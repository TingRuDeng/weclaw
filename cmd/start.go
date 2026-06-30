package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/config"
	feishuplatform "github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

var (
	foregroundFlag bool
	apiAddrFlag    string
)

var codexACPStartupRetryDelay = 2 * time.Second

func init() {
	startCmd.Flags().BoolVarP(&foregroundFlag, "foreground", "f", false, "Run in foreground (default is background)")
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "API server listen address (default 127.0.0.1:18011)")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the message bridge (auto-login WeChat if needed)",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !foregroundFlag {
		// Check if login is needed — if so, do it in foreground first, then daemon
		accounts, _ := ilink.LoadAllCredentials()
		if wechatEnabled(cfg) && len(accounts) == 0 {
			fmt.Println("No WeChat accounts found, starting login...")
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			_, err := doLogin(ctx)
			cancel()
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
		}
		return runDaemon()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load all accounts
	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	// No accounts — trigger login
	if wechatEnabled(cfg) && len(accounts) == 0 {
		log.Println("No WeChat accounts found, starting login...")
		creds, err := doLogin(ctx)
		if err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
		accounts = append(accounts, creds)
	}

	if config.DetectAndConfigure(cfg) {
		if err := config.Save(cfg); err != nil {
			log.Printf("Warning: failed to save auto-detected config: %v", err)
		} else {
			path, _ := config.ConfigPath()
			log.Printf("Auto-detected agents saved to %s", path)
		}
	}

	// Log all available agents
	if len(cfg.Agents) > 0 {
		names := make([]string, 0, len(cfg.Agents))
		for name := range cfg.Agents {
			names = append(names, name)
		}
		log.Printf("Available agents: %v (default: %s)", names, cfg.DefaultAgent)
	}

	// Create handler with an agent factory for on-demand agent creation
	handler := messaging.NewHandler(
		func(ctx context.Context, name string) agent.Agent {
			return createAgentByName(ctx, cfg, name)
		},
		func(name string) error {
			cfg.DefaultAgent = name
			return config.Save(cfg)
		},
	)

	// Populate agent metas for /status
	var metas []messaging.AgentMeta
	workDirs := make(map[string]string, len(cfg.Agents))
	for name, agCfg := range cfg.Agents {
		command := agCfg.Command
		if agCfg.Type == "http" {
			command = agCfg.Endpoint
		}
		metas = append(metas, messaging.AgentMeta{
			Name:    name,
			Type:    agCfg.Type,
			Command: command,
			Model:   agCfg.Model,
			Effort:  agCfg.Effort,
		})
		if agCfg.Cwd != "" {
			workDirs[name] = agCfg.Cwd
		}
	}
	handler.SetAgentMetas(metas)
	handler.SetAgentWorkDirs(workDirs)
	handler.SetProgressConfig(cfg.Progress)
	handler.SetAgentProgressConfigs(extractAgentProgressConfigs(cfg.Agents))
	handler.SetPlatformProgressConfigs(extractPlatformProgressConfigs(cfg.Platforms))
	handler.SetPlatformDefaultAgents(extractPlatformDefaultAgents(cfg.Platforms))
	handler.SetCodexSessionFile(messaging.DefaultCodexSessionFile())
	handler.SetClaudeSessionFile(messaging.DefaultClaudeSessionFile())

	// Load custom aliases from agent configs
	handler.SetCustomAliases(config.BuildAliasMap(cfg.Agents))

	// Set save directory for images/files if configured
	if cfg.SaveDir != "" {
		handler.SetSaveDir(cfg.SaveDir)
		log.Printf("Image save directory: %s", cfg.SaveDir)
	}

	// Start default agent initialization in background so monitors can start immediately
	go func() {
		if cfg.DefaultAgent == "" {
			log.Println("No default agent configured, staying in echo mode")
			return
		}
		log.Printf("Initializing default agent %q in background...", cfg.DefaultAgent)
		ag, err := handler.EnsureAgentStarted(ctx, cfg.DefaultAgent)
		if err != nil {
			log.Printf("Failed to initialize default agent %q, staying in echo mode: %v", cfg.DefaultAgent, err)
		} else {
			handler.SetDefaultAgent(cfg.DefaultAgent, ag)
		}
	}()

	// Build platform registry before HTTP API so active sending and inbound bridge share the same platform set.
	registry, err := buildPlatformRegistry(accounts, cfg)
	if err != nil {
		return err
	}
	go runSoftConfigReloader(ctx, handler, registry)

	// Resolve API addr: flag > env/config > default
	apiAddr := cfg.APIAddr // already includes env override from loadEnv
	if apiAddrFlag != "" {
		apiAddr = apiAddrFlag
	}
	apiServer := api.NewServer(nil, apiAddr, api.WithToken(cfg.APIToken), api.WithRegistry(registry))
	if err := apiServer.Validate(); err != nil {
		return err
	}
	go func() {
		if err := apiServer.Run(ctx); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	// Start platforms immediately — they will use echo mode until agent is ready
	log.Printf("Starting message bridge...")
	if err := registry.Run(ctx, handler.HandleMessage); err != nil {
		return err
	}
	log.Println("All platforms stopped")
	return nil
}

func buildPlatformRegistry(accounts []*ilink.Credentials, cfg *config.Config) (*platform.Registry, error) {
	entries := make([]platform.RegistryEntry, 0, len(accounts)+1)
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	if !wechatEnabled(cfg) {
		log.Printf("[platform] wechat disabled by config")
	} else {
		for _, creds := range accounts {
			adapter := wechat.NewAdapter(creds)
			adapter.SetAggregationWindow(wechatAggregationWindow(wechatCfg))
			entries = append(entries, platform.RegistryEntry{
				Platform: adapter,
				Access:   platform.NewAccessControl(wechatCfg.AllowedUsers),
			})
		}
	}
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	if feishuCfg.Enabled != nil && *feishuCfg.Enabled {
		creds, err := feishuplatform.LoadCredentials()
		if err != nil {
			return nil, fmt.Errorf("load feishu credentials: %w", err)
		}
		adapter := feishuplatform.NewAdapter(creds)
		adapter.SetSessionOptions(feishuplatform.FeishuSessionOptions{
			RequireMentionInGroup: feishuCfg.EffectiveRequireMentionInGroup(),
			ThreadIsolation:       feishuCfg.EffectiveThreadIsolation(),
		})
		entries = append(entries, platform.RegistryEntry{
			Platform: adapter,
			Access:   platform.NewAccessControl(feishuCfg.AllowedUsers),
		})
	}
	return platform.NewRegistry(entries), nil
}

func wechatEnabled(cfg *config.Config) bool {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	return wechatCfg.Enabled == nil || *wechatCfg.Enabled
}

func wechatAggregationWindow(cfg config.PlatformConfig) time.Duration {
	if cfg.MessageAggregationMs == nil {
		return 800 * time.Millisecond
	}
	if *cfg.MessageAggregationMs <= 0 {
		return 0
	}
	return time.Duration(*cfg.MessageAggregationMs) * time.Millisecond
}

// createAgentByName creates and starts an agent by its config name.
// Returns nil if the agent is not configured or fails to start.
func createAgentByName(ctx context.Context, cfg *config.Config, name string) agent.Agent {
	agCfg, ok := cfg.Agents[name]
	if !ok {
		log.Printf("[agent] %q not found in config", name)
		return nil
	}

	switch agCfg.Type {
	case "acp":
		ag, err := startACPAgentWithRetry(ctx, name, agCfg)
		if err != nil {
			log.Printf("[agent] failed to start ACP agent %q: %v", name, err)
			return nil
		}
		log.Printf("[agent] started ACP agent: %s (command=%s, type=%s, model=%s, effort=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model, agCfg.Effort)
		return ag
	case "cli":
		ag := agent.NewCLIAgent(agent.CLIAgentConfig{
			Name:         name,
			Command:      agCfg.Command,
			Args:         agCfg.Args,
			Cwd:          agCfg.Cwd,
			Env:          agCfg.Env,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
			RunAsUser:    agCfg.RunAsUser,
			RunAsEnv:     agCfg.RunAsEnv,
		})
		log.Printf("[agent] created CLI agent: %s (command=%s, type=%s, model=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model)
		return ag
	case "http":
		if agCfg.Endpoint == "" {
			log.Printf("[agent] HTTP agent %q has no endpoint", name)
			return nil
		}
		ag := agent.NewHTTPAgent(agent.HTTPAgentConfig{
			Endpoint:     agCfg.Endpoint,
			APIKey:       agCfg.APIKey,
			Headers:      agCfg.Headers,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
			MaxHistory:   agCfg.MaxHistory,
		})
		log.Printf("[agent] created HTTP agent: %s (endpoint=%s, model=%s)", name, agCfg.Endpoint, agCfg.Model)
		return ag
	case "companion":
		if agCfg.Command == "" {
			log.Printf("[agent] companion agent %q has no command", name)
			return nil
		}
		ag := agent.NewCompanionAgent(agent.CompanionAgentConfig{
			Name:       name,
			Command:    agCfg.Command,
			Args:       agCfg.Args,
			Cwd:        agCfg.Cwd,
			Env:        agCfg.Env,
			Model:      agCfg.Model,
			AutoLaunch: companionAutoLaunchEnabled(name, agCfg),
		})
		if err := ag.Start(ctx); err != nil {
			log.Printf("[agent] failed to start companion agent %q: %v", name, err)
			return nil
		}
		log.Printf("[agent] started companion agent: %s (command=%s, type=%s)", name, agCfg.Command, agCfg.Type)
		return ag
	default:
		log.Printf("[agent] unknown type %q for %q", agCfg.Type, name)
		return nil
	}
}

func startACPAgentWithRetry(ctx context.Context, name string, agCfg config.AgentConfig) (*agent.ACPAgent, error) {
	attempts := 1
	if isCodexAppServerAgent(agCfg) {
		attempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ag := newACPAgentFromConfig(agCfg)
		if err := ag.Start(ctx); err != nil {
			lastErr = err
			if attempt == attempts || !isRetryableCodexStateRuntimeError(err) {
				return nil, err
			}
			log.Printf("[agent] retrying Codex ACP startup after sqlite state runtime error (agent=%s, attempt=%d/%d): %v", name, attempt+1, attempts, err)
			if err := sleepContext(ctx, codexACPStartupRetryDelay); err != nil {
				return nil, err
			}
			continue
		}
		return ag, nil
	}
	return nil, lastErr
}

func newACPAgentFromConfig(agCfg config.AgentConfig) *agent.ACPAgent {
	return agent.NewACPAgent(agent.ACPAgentConfig{
		Command:      agCfg.Command,
		Args:         agCfg.Args,
		Cwd:          agCfg.Cwd,
		Env:          agCfg.Env,
		Model:        agCfg.Model,
		Effort:       agCfg.Effort,
		SystemPrompt: agCfg.SystemPrompt,
		RunAsUser:    agCfg.RunAsUser,
		RunAsEnv:     agCfg.RunAsEnv,
	})
}

func isCodexAppServerAgent(agCfg config.AgentConfig) bool {
	if filepath.Base(agCfg.Command) != "codex" {
		return false
	}
	for _, arg := range agCfg.Args {
		if arg == "app-server" {
			return true
		}
	}
	return false
}

func isRetryableCodexStateRuntimeError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "failed to initialize sqlite state runtime") ||
		strings.Contains(text, "failed to initialize state runtime")
}

func companionAutoLaunchEnabled(_ string, agCfg config.AgentConfig) bool {
	if agCfg.AutoLaunch != nil {
		return *agCfg.AutoLaunch
	}
	return false
}

func extractAgentProgressConfigs(agents map[string]config.AgentConfig) map[string]config.ProgressConfig {
	progressConfigs := make(map[string]config.ProgressConfig)
	for name, agentConfig := range agents {
		if agentConfig.Progress == nil {
			continue
		}
		progressConfigs[name] = *agentConfig.Progress
	}
	return progressConfigs
}

func extractPlatformProgressConfigs(platforms map[string]config.PlatformConfig) map[string]config.ProgressConfig {
	progressConfigs := make(map[string]config.ProgressConfig)
	for name, platformConfig := range platforms {
		if platformConfig.Progress == nil {
			continue
		}
		progressConfigs[name] = *platformConfig.Progress
	}
	return progressConfigs
}

func extractPlatformDefaultAgents(platforms map[string]config.PlatformConfig) map[string]string {
	defaultAgents := make(map[string]string)
	for name, platformConfig := range platforms {
		if platformConfig.DefaultAgent == "" {
			continue
		}
		defaultAgents[name] = platformConfig.DefaultAgent
	}
	return defaultAgents
}

func runSoftConfigReloader(ctx context.Context, handler *messaging.Handler, registry *platform.Registry) {
	path, err := config.ConfigPath()
	if err != nil {
		log.Printf("[config] WARNING: cannot resolve config path for hot reload: %v", err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	lastMod := info.ModTime()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil || !info.ModTime().After(lastMod) {
				continue
			}
			next, err := config.Load()
			if err != nil {
				log.Printf("[config] WARNING: hot reload failed, keeping previous config: %v", err)
				lastMod = info.ModTime()
				continue
			}
			applySoftConfig(handler, registry, next)
			lastMod = info.ModTime()
			log.Printf("[config] soft config reloaded from %s", path)
		}
	}
}

func applySoftConfig(handler *messaging.Handler, registry *platform.Registry, cfg *config.Config) {
	if handler == nil || cfg == nil {
		return
	}
	handler.SetProgressConfig(cfg.Progress)
	handler.SetAgentProgressConfigs(extractAgentProgressConfigs(cfg.Agents))
	handler.SetPlatformProgressConfigs(extractPlatformProgressConfigs(cfg.Platforms))
	handler.SetPlatformDefaultAgents(extractPlatformDefaultAgents(cfg.Platforms))
	if cfg.DefaultAgent != "" {
		if ag := handler.AgentByName(cfg.DefaultAgent); ag != nil {
			handler.SetDefaultAgent(cfg.DefaultAgent, ag)
		}
	}
	for name, platformConfig := range cfg.Platforms {
		registry.UpdateAccess(platform.PlatformName(name), platformConfig.AllowedUsers)
	}
}

// doLogin runs the interactive QR login flow and returns credentials.
func doLogin(ctx context.Context) (*ilink.Credentials, error) {
	fmt.Println("Fetching QR code...")
	qr, err := ilink.FetchQRCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch QR code: %w", err)
	}

	fmt.Println("\nScan this QR code with WeChat:")
	fmt.Println()
	qrterminal.GenerateWithConfig(qr.QRCodeImgContent, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
	})
	fmt.Printf("\nQR URL: %s\n", qr.QRCodeImgContent)
	fmt.Println("\nWaiting for scan...")

	lastStatus := ""
	creds, err := ilink.PollQRStatus(ctx, qr.QRCode, func(status string) {
		if status != lastStatus {
			lastStatus = status
			switch status {
			case "scaned":
				fmt.Println("QR code scanned! Please confirm on your phone.")
			case "confirmed":
				fmt.Println("Login confirmed!")
			case "expired":
				fmt.Println("QR code expired.")
			}
		}
	})
	if err != nil {
		return nil, err
	}

	if err := ilink.SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("failed to save credentials: %w", err)
	}

	dir, _ := ilink.CredentialsPath()
	fmt.Printf("\nLogin successful! Credentials saved to %s\n", dir)
	fmt.Printf("Bot ID: %s\n\n", creds.ILinkBotID)
	return creds, nil
}

// --- Daemon mode ---

func weclawDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weclaw")
}

func pidFile() string {
	return filepath.Join(weclawDir(), "weclaw.pid")
}

func logFile() string {
	return filepath.Join(weclawDir(), "weclaw.log")
}

const (
	gracefulStopChecks   = 20
	gracefulStopInterval = 500 * time.Millisecond
)

// runDaemon spawns weclaw start (without --daemon) as a background process.
func runDaemon() error {
	if err := stopAllWeclaw(); err != nil {
		return err
	}
	if err := agent.CleanupCompanionEndpoints(); err != nil {
		return fmt.Errorf("cleanup companion endpoints: %w", err)
	}

	// Ensure log directory exists
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return fmt.Errorf("create weclaw dir: %w", err)
	}

	// Open log file
	lf, err := os.OpenFile(logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Re-exec ourselves without --daemon
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	cmd := exec.Command(exe, "start", "-f")
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	pid := cmd.Process.Pid
	if err := os.WriteFile(pidFile(), []byte(fmt.Sprintf("%d", pid)), 0o644); err != nil {
		lf.Close()
		return handleDaemonPIDWriteResult(err, daemonPIDWriteProcess{
			kill:    cmd.Process.Kill,
			wait:    cmd.Wait,
			release: cmd.Process.Release,
		})
	}

	// Detach — don't wait
	if err := cmd.Process.Release(); err != nil {
		lf.Close()
		return fmt.Errorf("release daemon process: %w", err)
	}
	lf.Close()

	fmt.Printf("weclaw started in background (pid=%d)\n", pid)
	fmt.Printf("Log: %s\n", logFile())
	fmt.Printf("Stop: weclaw stop\n")
	return nil
}

type daemonPIDWriteProcess struct {
	kill    func() error
	wait    func() error
	release func() error
}

// handleDaemonPIDWriteResult 在 pid 文件写入失败时回收刚启动的进程，避免后台服务失控。
func handleDaemonPIDWriteResult(writeErr error, proc daemonPIDWriteProcess) error {
	if writeErr == nil {
		if proc.release != nil {
			return proc.release()
		}
		return nil
	}
	if proc.kill != nil {
		_ = proc.kill()
	}
	if proc.wait != nil {
		_ = proc.wait()
	}
	return fmt.Errorf("write pid file: %w", writeErr)
}

func readPid() (int, error) {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without killing it
	return p.Signal(syscall.Signal(0)) == nil
}

type stopProcessOps struct {
	readPid            func() (int, error)
	processExists      func(int) bool
	signalPID          func(int, syscall.Signal) error
	signalProcessGroup func(int, syscall.Signal) error
	removePIDFile      func() error
	sleep              func(time.Duration)
}

func stopAllWeclaw() error {
	return stopAllWeclawWithOps(defaultStopProcessOps())
}

func defaultStopProcessOps() stopProcessOps {
	return stopProcessOps{
		readPid:            readPid,
		processExists:      processExists,
		signalPID:          signalPID,
		signalProcessGroup: signalProcessGroup,
		removePIDFile: func() error {
			return os.Remove(pidFile())
		},
		sleep: time.Sleep,
	}
}

// stopAllWeclawWithOps 只停止 pid 文件指向的目标，避免按命令行扫描误杀其他进程。
func stopAllWeclawWithOps(ops stopProcessOps) error {
	pid, err := ops.readPid()
	if err != nil {
		return nil
	}
	if !ops.processExists(pid) {
		return ops.removePIDFile()
	}
	_ = ops.signalPID(pid, syscall.SIGTERM)
	if waitProcessExit(pid, ops) {
		return ops.removePIDFile()
	}

	_ = ops.signalProcessGroup(pid, syscall.SIGKILL)
	_ = ops.signalPID(pid, syscall.SIGKILL)
	if waitProcessExit(pid, ops) {
		return ops.removePIDFile()
	}
	return fmt.Errorf("weclaw process pid=%d did not exit", pid)
}

func waitProcessExit(pid int, ops stopProcessOps) bool {
	for i := 0; i < gracefulStopChecks; i++ {
		ops.sleep(gracefulStopInterval)
		if !ops.processExists(pid) {
			return true
		}
	}
	return false
}

func signalPID(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}
