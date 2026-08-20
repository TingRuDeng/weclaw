package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/internal/auththrottle"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

const (
	httpReadHeaderTimeout = 5 * time.Second
	httpReadTimeout       = 15 * time.Second
	httpWriteTimeout      = 5 * time.Minute
	httpIdleTimeout       = 60 * time.Second
)

// Server provides an HTTP API for sending messages.
type Server struct {
	clients  []*ilink.Client
	registry *platform.Registry
	status   RuntimeStatusProvider
	drain    RuntimeDrainController
	restart  RuntimeRestartController
	accounts CodexAccountController
	codexCLI CodexCLIHostController
	traces   observability.QueryProvider
	outbox   TerminalOutboxController
	addr     string
	token    string
	sendAuth *auththrottle.Throttle
}

// RuntimeStatusProvider 暴露服务进程内的轻量运行态，供本机 CLI 做重启保护。
type RuntimeStatusProvider interface {
	ActiveTaskCount() int
}

// RuntimeDrainController 在本机重启前原子停止任务接纳并排空活动任务。
type RuntimeDrainController interface {
	Drain(context.Context, bool) (messaging.RuntimeDrainResult, error)
	CancelDrain()
}

// RuntimeRestartController owns the stronger service-plus-Codex restart
// transaction. It remains separate from generic task draining.
type RuntimeRestartController interface {
	PrepareRuntimeRestart(context.Context, bool) (messaging.RuntimeRestartResult, error)
	CancelRuntimeRestart(context.Context) error
}

// RuntimeRestartOptionsController lets newer controllers receive explicit
// restart authority without changing the legacy loopback controller contract.
type RuntimeRestartOptionsController interface {
	PrepareRuntimeRestartWithOptions(context.Context, bool, bool) (messaging.RuntimeRestartResult, error)
}

// CodexAccountController 由消息层实现，统一协调运行中的任务、Agent 与账号事务。
type CodexAccountController interface {
	ListCodexAccounts(context.Context) (agent.CodexAccountStatus, error)
	CurrentCodexAccount(context.Context, bool) (agent.CodexAccountStatus, error)
	SaveCodexAccount(context.Context, agent.CodexAccountSaveOptions) (agent.CodexAccountProfile, error)
	UseCodexAccount(context.Context, string, uint64) (agent.CodexAccountSwitchResult, error)
	RemoveCodexAccount(context.Context, string) error
	DoctorCodexAccounts(context.Context) codexauth.DoctorResult
}

// CodexCLIHostController prepares the one official daemon through the running
// service's Agent instance before a local terminal frontend connects.
type CodexCLIHostController interface {
	PrepareCodexCLIHost(context.Context) (agent.CodexCLIHost, error)
}

// TerminalOutboxController 仅暴露脱敏状态和幂等重投调度，不允许读取消息正文或路由。
type TerminalOutboxController interface {
	TerminalOutboxStatus(context.Context) (messaging.TerminalOutboxStatus, error)
	RedriveTerminalOutbox(context.Context, string) (messaging.TerminalOutboxRedriveResult, error)
}

// Option 调整 API 服务运行参数，避免构造函数继续膨胀。
type Option func(*Server)

// WithToken 配置发送 API 的鉴权 token。
func WithToken(token string) Option {
	return func(s *Server) {
		s.token = strings.TrimSpace(token)
	}
}

// WithRegistry 配置主动发送 API 使用统一平台注册表定位出站会话。
func WithRegistry(registry *platform.Registry) Option {
	return func(s *Server) {
		s.registry = registry
	}
}

// WithRuntimeStatusProvider 配置只读运行态来源。
func WithRuntimeStatusProvider(provider RuntimeStatusProvider) Option {
	return func(s *Server) {
		s.status = provider
	}
}

// WithRuntimeDrainController 配置只允许本机调用的安全重启排空入口。
func WithRuntimeDrainController(controller RuntimeDrainController) Option {
	return func(s *Server) {
		s.drain = controller
	}
}

// WithRuntimeRestartController configures the loopback-only coordinated
// service and Codex Host restart entrypoint.
func WithRuntimeRestartController(controller RuntimeRestartController) Option {
	return func(s *Server) {
		s.restart = controller
	}
}

// WithCodexAccountController 配置仅本机可访问的 Codex 账号控制器。
func WithCodexAccountController(controller CodexAccountController) Option {
	return func(s *Server) {
		s.accounts = controller
	}
}

// WithCodexCLIHostController configures the local controlled-CLI entrypoint.
func WithCodexCLIHostController(controller CodexCLIHostController) Option {
	return func(s *Server) {
		s.codexCLI = controller
	}
}

// WithTraceQueryProvider 配置只允许本机查询的结构化诊断 Trace。
func WithTraceQueryProvider(provider observability.QueryProvider) Option {
	return func(s *Server) {
		s.traces = provider
	}
}

// WithTerminalOutboxController 配置仅本机可访问的终态投递运维控制器。
func WithTerminalOutboxController(controller TerminalOutboxController) Option {
	return func(s *Server) {
		s.outbox = controller
	}
}

// NewServer creates an API server.
func NewServer(clients []*ilink.Client, addr string, options ...Option) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	server := &Server{clients: clients, addr: addr, sendAuth: auththrottle.New()}
	for _, option := range options {
		option(server)
	}
	return server
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/runtime", s.handleRuntimeStatus)
	mux.HandleFunc("/api/runtime/drain", s.handleRuntimeDrain)
	mux.HandleFunc("/api/runtime/restart/prepare", s.handleRuntimeRestart)
	mux.HandleFunc("/api/traces", s.handleTraceQuery)
	mux.HandleFunc("/api/terminal-outbox", s.handleTerminalOutboxStatus)
	mux.HandleFunc("/api/terminal-outbox/redrive", s.handleTerminalOutboxRedrive)
	mux.HandleFunc("/api/codex/accounts", s.handleCodexAccounts)
	mux.HandleFunc("/api/codex/accounts/current", s.handleCodexAccountCurrent)
	mux.HandleFunc("/api/codex/accounts/save", s.handleCodexAccountSave)
	mux.HandleFunc("/api/codex/accounts/use", s.handleCodexAccountUse)
	mux.HandleFunc("/api/codex/accounts/remove", s.handleCodexAccountRemove)
	mux.HandleFunc("/api/codex/accounts/doctor", s.handleCodexAccountDoctor)
	mux.HandleFunc("/api/codex/cli/prepare", s.handleCodexCLIPrepare)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := newHTTPServer(s.addr, mux)

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("[api] listening on %s", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleRuntimeRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeLocalControl(w, r) {
		return
	}
	if s.restart == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "runtime_restart_unavailable", "协调重启入口不可用")
		return
	}
	if r.Method == http.MethodDelete {
		cancelCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.restart.CancelRuntimeRestart(cancelCtx); err != nil {
			writeJSONStatus(w, http.StatusConflict, map[string]any{
				"status": "error", "code": "runtime_restart_cancel_failed",
				"message": observability.SanitizeText(err.Error()), "draining": s.runtimeRestartDraining(),
			})
			return
		}
		writeJSONResponse(w, map[string]any{"status": "ok", "draining": false})
		return
	}
	force := false
	if raw := strings.TrimSpace(r.URL.Query().Get("force")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_force", "force 必须是布尔值")
			return
		}
		force = parsed
	}
	stopConflictingCodexHosts := false
	if raw := strings.TrimSpace(r.URL.Query().Get("stop_conflicting_codex_hosts")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_stop_conflicting_codex_hosts", "stop_conflicting_codex_hosts 必须是布尔值")
			return
		}
		stopConflictingCodexHosts = parsed
	}
	restartCtx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var result messaging.RuntimeRestartResult
	var err error
	if controller, ok := s.restart.(RuntimeRestartOptionsController); ok {
		result, err = controller.PrepareRuntimeRestartWithOptions(restartCtx, force, stopConflictingCodexHosts)
	} else {
		result, err = s.restart.PrepareRuntimeRestart(restartCtx, force)
	}
	if errors.Is(err, messaging.ErrActiveTasksRunning) || errors.Is(err, messaging.ErrRuntimeRestartBlocked) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"status": "busy", "draining": s.runtimeRestartDraining(),
			"active_tasks": result.ActiveTasks, "remaining_tasks": result.RemainingTasks,
			"message": observability.SanitizeText(err.Error()),
		})
		return
	}
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"status": "error", "code": "runtime_restart_failed",
			"message": observability.SanitizeText(err.Error()), "draining": s.runtimeRestartDraining(),
		})
		return
	}
	writeJSONResponse(w, map[string]any{
		"status": "ok", "draining": true,
		"active_tasks": result.ActiveTasks, "remaining_tasks": result.RemainingTasks,
		"codex": result.Codex, "codex_host": result.CodexHost,
	})
}

func (s *Server) runtimeRestartDraining() bool {
	provider, ok := s.restart.(interface{ IsDraining() bool })
	return ok && provider.IsDraining()
}

func (s *Server) handleRuntimeDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeLocalControl(w, r) {
		return
	}
	if s.drain == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "runtime_drain_unavailable", "安全重启排空入口不可用")
		return
	}
	if r.Method == http.MethodDelete {
		s.drain.CancelDrain()
		writeJSONResponse(w, map[string]any{"status": "ok", "draining": false})
		return
	}
	force := false
	if raw := strings.TrimSpace(r.URL.Query().Get("force")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_force", "force 必须是布尔值")
			return
		}
		force = parsed
	}
	drainCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := s.drain.Drain(drainCtx, force)
	if errors.Is(err, messaging.ErrActiveTasksRunning) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"status": "busy", "draining": false,
			"active_tasks": result.ActiveTasks, "remaining_tasks": result.RemainingTasks,
		})
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "runtime_drain_failed", observability.SanitizeText(err.Error()))
		return
	}
	writeJSONResponse(w, map[string]any{
		"status": "ok", "draining": true,
		"active_tasks": result.ActiveTasks, "remaining_tasks": result.RemainingTasks,
	})
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
}

// Validate 在监听暴露到非本机地址前要求显式 token，避免发送 API 被未授权调用。
func (s *Server) Validate() error {
	if s.token != "" || isLoopbackListenAddr(s.addr) {
		return nil
	}
	return fmt.Errorf("api token is required when api_addr %q is not loopback", s.addr)
}

func (s *Server) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRead(w, r) {
		return
	}
	activeTasks := 0
	if s.status != nil {
		activeTasks = s.status.ActiveTaskCount()
	}
	response := map[string]any{
		"status":       "ok",
		"active_tasks": activeTasks,
	}
	if s.traces != nil {
		response["trace"] = s.traces.Status()
	}
	writeJSONResponse(w, response)
}

func (s *Server) handleTraceQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeLocalControl(w, r) {
		return
	}
	if s.traces == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "trace_unavailable", "Trace 未启用")
		return
	}
	query, err := parseTraceQuery(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_trace_query", err.Error())
		return
	}
	page, err := s.traces.Query(r.Context(), query)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "trace_query_failed", observability.SanitizeText(err.Error()))
		return
	}
	writeJSONResponse(w, page)
}

func parseTraceQuery(r *http.Request) (observability.Query, error) {
	values := r.URL.Query()
	query := observability.Query{
		TraceID: values.Get("trace_id"), MessageID: values.Get("message_id"),
		TaskID: values.Get("task_id"), ThreadID: values.Get("thread_id"),
		TurnID: values.Get("turn_id"), Stage: values.Get("stage"),
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > 1000 {
			return observability.Query{}, fmt.Errorf("limit 必须在 1 到 1000 之间")
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(values.Get("since")); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return observability.Query{}, fmt.Errorf("since 必须是 RFC3339 时间")
		}
		query.Since = since
	}
	return query, nil
}
