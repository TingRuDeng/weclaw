package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

type startRuntime struct {
	ctx      context.Context
	cfg      *config.Config
	handler  *messaging.Handler
	registry *platform.Registry
	trace    *observability.Store
}

type backgroundStartOps struct {
	loadAccounts func() ([]*ilink.Credentials, error)
	login        func(context.Context) (*ilink.Credentials, error)
	runDaemon    func() error
}

// runBackgroundStart 在派生后台进程前完成必要的微信登录。
func runBackgroundStart(cfg *config.Config) error {
	return runBackgroundStartWithOps(cfg, backgroundStartOps{
		loadAccounts: ilink.LoadAllCredentials,
		login:        doLogin,
		runDaemon:    runDaemon,
	})
}

// runBackgroundStartWithOps 保证凭据加载、登录和 daemon 启动按失败边界顺序执行。
func runBackgroundStartWithOps(cfg *config.Config, ops backgroundStartOps) error {
	accounts, err := ops.loadAccounts()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}
	if !wechatEnabled(cfg) || len(accounts) > 0 {
		return ops.runDaemon()
	}
	fmt.Println("未找到微信账号，正在启动微信扫码登录...")
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if _, err := ops.login(ctx); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	return ops.runDaemon()
}

// runForegroundStart 依次准备状态、账号、Agent、平台与服务运行时。
func runForegroundStart(cfg *config.Config) error {
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
	accounts, err := loadStartAccounts(ctx, cfg)
	if err != nil {
		return err
	}
	if err := detectStartAgents(cfg); err != nil {
		return err
	}
	traceStore := newStartTraceStore()
	handler := newStartHandlerWithTrace(cfg, traceStore)
	startDefaultAgent(ctx, handler, cfg)
	registry, err := newStartRegistry(accounts, cfg, handler)
	if err != nil {
		return err
	}
	runtime := startRuntime{ctx: ctx, cfg: cfg, handler: handler, registry: registry, trace: traceStore}
	if err := runtime.startServices(); err != nil {
		return err
	}
	return runtime.runBridge()
}

// loadStartAccounts 加载微信账号，并在启用微信但无账号时发起登录。
func loadStartAccounts(ctx context.Context, cfg *config.Config) ([]*ilink.Credentials, error) {
	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials: %w", err)
	}
	if !wechatEnabled(cfg) || len(accounts) > 0 {
		return accounts, nil
	}
	log.Println("未找到微信账号，正在启动微信扫码登录...")
	creds, err := doLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}
	return append(accounts, creds), nil
}

// detectStartAgents 自动探测 Agent，并在保存后再次验证 Claude ACP 配置。
func detectStartAgents(cfg *config.Config) error {
	if config.DetectAndConfigure(cfg) {
		if err := config.Save(cfg); err != nil {
			log.Printf("Warning: failed to save auto-detected config: %v", err)
		} else {
			path, _ := config.ConfigPath()
			log.Printf("Auto-detected agents saved to %s", path)
		}
	}
	return cfg.ValidateClaudeACPAgents()
}

func newStartHandlerWithTrace(cfg *config.Config, traceStore *observability.Store) *messaging.Handler {
	var protocolTrace observability.ProtocolRecorder
	if traceStore != nil && codexProtocolTraceEnabled() {
		protocolTrace = traceStore
	}
	logAvailableAgents(cfg)
	handler := messaging.NewHandler(func(ctx context.Context, name string) agent.Agent {
		return createAgentByName(ctx, cfg, name, protocolTrace)
	}, saveDefaultAgent)
	handler.SetTraceRecorder(traceStore)
	configureHandlerMetadata(handler, cfg)
	configureHandlerState(handler)
	configureHandlerAccess(handler, cfg)
	return handler
}

func newStartTraceStore() *observability.Store {
	path := observability.DefaultPath()
	store, err := observability.NewStore(observability.StoreOptions{
		Path: path, IncludeProtocolPayload: envBool("WECLAW_CODEX_PROTOCOL_TRACE_PAYLOAD"),
	})
	if err != nil {
		log.Printf("[trace] disabled: %v", err)
		return nil
	}
	return store
}

func codexProtocolTraceEnabled() bool {
	return envBool("WECLAW_CODEX_PROTOCOL_TRACE") || envBool("WECLAW_CODEX_PROTOCOL_TRACE_PAYLOAD")
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// logAvailableAgents 记录可用 Agent，便于确认自动探测结果。
func logAvailableAgents(cfg *config.Config) {
	if len(cfg.Agents) == 0 {
		return
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	log.Printf("Available agents: %v (default: %s)", names, cfg.DefaultAgent)
}

// configureHandlerMetadata 配置 Agent 元数据、工作目录和进度策略。
func configureHandlerMetadata(handler *messaging.Handler, cfg *config.Config) {
	metas := make([]messaging.AgentMeta, 0, len(cfg.Agents))
	workDirs := make(map[string]string, len(cfg.Agents))
	for name, agCfg := range cfg.Agents {
		command := agCfg.Command
		if agCfg.Type == "http" {
			command = agCfg.Endpoint
		}
		metas = append(metas, messaging.AgentMeta{
			Name: name, Type: agCfg.Type, Command: command, Model: agCfg.Model, Effort: agCfg.Effort,
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
	handler.SetCustomAliases(config.BuildAliasMap(cfg.Agents))
}

// configureHandlerState 装载路由、Codex、Claude 与飞书身份状态文件。
func configureHandlerState(handler *messaging.Handler) {
	if err := handler.SetAgentSessionFile(messaging.DefaultAgentSessionFile()); err != nil {
		log.Printf("加载会话 Agent 状态失败：%v", err)
	}
	handler.SetCodexSessionFile(messaging.DefaultCodexSessionFile())
	handler.SetFeishuIdentityFile(messaging.DefaultFeishuIdentityFile())
	if err := handler.SetClaudeSessionFile(messaging.DefaultClaudeSessionFile()); err != nil {
		log.Printf("加载 Claude 会话状态失败：%v", err)
	}
}

// configureHandlerAccess 配置文件保存、工作区权限、限流与审计。
func configureHandlerAccess(handler *messaging.Handler, cfg *config.Config) {
	if cfg.SaveDir != "" {
		handler.SetSaveDir(cfg.SaveDir)
		log.Printf("Image save directory: %s", cfg.SaveDir)
	}
	handler.SetAllowedWorkspaceRoots(cfg.AllowedWorkspaceRoots)
	if len(cfg.AllowedWorkspaceRoots) == 0 {
		log.Printf("WARNING: allowed_workspace_roots 未配置，普通用户远程 /cwd 切换已禁用；管理员不受此限制。")
	} else {
		log.Printf("Allowed workspace roots: %v", cfg.AllowedWorkspaceRoots)
	}
	handler.SetAdminUsers(cfg.AdminUsers)
	log.Printf("Admin users configured: %d", len(cfg.AdminUsers))
	handler.SetRateLimitPerMinute(cfg.RateLimitPerMinute)
	if cfg.RateLimitPerMinute > 0 {
		log.Printf("Rate limit: %d agent invocations per user per minute", cfg.RateLimitPerMinute)
	}
	configureHandlerAudit(handler, cfg)
}

// configureHandlerAudit 按配置启用文件审计日志。
func configureHandlerAudit(handler *messaging.Handler, cfg *config.Config) {
	if cfg.AuditLog != nil && !*cfg.AuditLog {
		return
	}
	auditPath := cfg.AuditLogPath
	if auditPath == "" {
		auditPath = messaging.DefaultAuditLogPath()
	}
	handler.SetAuditLogger(messaging.NewFileAuditLogger(auditPath))
	log.Printf("Audit log: %s", auditPath)
}

// startDefaultAgent 后台初始化默认 Agent，使平台监听无需等待握手完成。
func startDefaultAgent(ctx context.Context, handler *messaging.Handler, cfg *config.Config) {
	go func() {
		if cfg.DefaultAgent == "" {
			log.Println("No default agent configured, staying in echo mode")
			return
		}
		log.Printf("Initializing default agent %q in background...", cfg.DefaultAgent)
		ag, err := handler.EnsureAgentStarted(ctx, cfg.DefaultAgent)
		if err != nil {
			log.Printf("Failed to initialize default agent %q, staying in echo mode: %v", cfg.DefaultAgent, err)
			return
		}
		handler.SetDefaultAgent(cfg.DefaultAgent, ag)
	}()
}

// newStartRegistry 创建供主动发送、入站消息和身份观察共享的平台注册表。
func newStartRegistry(accounts []*ilink.Credentials, cfg *config.Config, handler *messaging.Handler) (*platform.Registry, error) {
	return buildPlatformRegistry(accounts, cfg,
		platform.WithIdentityObserver(handler.ObserveFeishuIdentity),
		platform.WithDenyNoticeProvider(handler.ObserveDeniedIdentity),
	)
}

// startServices 启动热加载、HTTP API 与重启完成通知。
func (runtime startRuntime) startServices() error {
	if err := runtime.handler.StartTerminalOutbox(runtime.ctx, runtime.registry, messaging.DefaultTerminalOutboxFile()); err != nil {
		return fmt.Errorf("start terminal outbox: %w", err)
	}
	go runSoftConfigReloader(runtime.ctx, runtime.handler, runtime.registry)
	apiServer := api.NewServer(nil, runtime.apiAddress(),
		api.WithToken(runtime.cfg.APIToken),
		api.WithRegistry(runtime.registry),
		api.WithRuntimeStatusProvider(runtime.handler),
		api.WithCodexAccountController(runtime.handler),
		api.WithTraceQueryProvider(runtime.trace),
	)
	if err := apiServer.Validate(); err != nil {
		return err
	}
	go func() {
		if err := apiServer.Run(runtime.ctx); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()
	go messaging.DeliverPendingRestartNotifications(runtime.ctx, runtime.registry, Version)
	return nil
}

// apiAddress 按命令行参数优先级解析 API 监听地址。
func (runtime startRuntime) apiAddress() string {
	if apiAddrFlag != "" {
		return apiAddrFlag
	}
	return runtime.cfg.APIAddr
}

// runBridge 运行平台消息桥，并在所有平台退出后结束前台进程。
func (runtime startRuntime) runBridge() error {
	log.Printf("Starting message bridge...")
	if err := runtime.registry.Run(runtime.ctx, runtime.handler.HandleMessage); err != nil {
		return err
	}
	log.Println("All platforms stopped")
	return nil
}
