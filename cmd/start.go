package cmd

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/spf13/cobra"
)

var (
	foregroundFlag bool
	apiAddrFlag    string
)

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

	runtimeLock, err := acquireRuntimeLock()
	if err != nil {
		return err
	}
	defer runtimeLock.Close()
	if err := writeCurrentRuntimeState(currentServiceMode()); err != nil {
		return fmt.Errorf("write runtime state: %w", err)
	}
	defer removeRuntimeState()

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
	handler.SetFeishuIdentityFile(messaging.DefaultFeishuIdentityFile())
	handler.SetClaudeSessionFile(messaging.DefaultClaudeSessionFile())

	// Load custom aliases from agent configs
	handler.SetCustomAliases(config.BuildAliasMap(cfg.Agents))

	// Set save directory for images/files if configured
	if cfg.SaveDir != "" {
		handler.SetSaveDir(cfg.SaveDir)
		log.Printf("Image save directory: %s", cfg.SaveDir)
	}

	handler.SetAllowedWorkspaceRoots(cfg.AllowedWorkspaceRoots)
	if len(cfg.AllowedWorkspaceRoots) == 0 {
		log.Printf("WARNING: allowed_workspace_roots 未配置，远程 /cwd 切换已禁用；如需切换工作区，请在 config.json 配置 allowed_workspace_roots。")
	} else {
		log.Printf("Allowed workspace roots: %v", cfg.AllowedWorkspaceRoots)
	}
	handler.SetAdminUsers(cfg.AdminUsers)
	log.Printf("Admin users configured: %d", len(cfg.AdminUsers))
	handler.SetRateLimitPerMinute(cfg.RateLimitPerMinute)
	if cfg.RateLimitPerMinute > 0 {
		log.Printf("Rate limit: %d agent invocations per user per minute", cfg.RateLimitPerMinute)
	}
	if cfg.AuditLog == nil || *cfg.AuditLog {
		auditPath := cfg.AuditLogPath
		if auditPath == "" {
			auditPath = messaging.DefaultAuditLogPath()
		}
		handler.SetAuditLogger(messaging.NewFileAuditLogger(auditPath))
		log.Printf("Audit log: %s", auditPath)
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
	registry, err := buildPlatformRegistry(accounts, cfg, platform.WithIdentityObserver(handler.ObserveFeishuIdentity))
	if err != nil {
		return err
	}
	go runSoftConfigReloader(ctx, handler, registry)

	// Resolve API addr: flag > env/config > default
	apiAddr := cfg.APIAddr // already includes env override from loadEnv
	if apiAddrFlag != "" {
		apiAddr = apiAddrFlag
	}
	apiServer := api.NewServer(
		nil,
		apiAddr,
		api.WithToken(cfg.APIToken),
		api.WithRegistry(registry),
		api.WithRuntimeStatusProvider(handler),
	)
	if err := apiServer.Validate(); err != nil {
		return err
	}
	go func() {
		if err := apiServer.Run(ctx); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	// 新进程启动到可主动发送阶段后，回写上一次远程重启的完成通知。
	go messaging.DeliverPendingRestartNotifications(ctx, registry, Version)

	// Start platforms immediately — they will use echo mode until agent is ready
	log.Printf("Starting message bridge...")
	if err := registry.Run(ctx, handler.HandleMessage); err != nil {
		return err
	}
	log.Println("All platforms stopped")
	return nil
}
