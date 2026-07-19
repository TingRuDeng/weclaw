package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/ilink"
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
	accounts CodexAccountController
	traces   observability.QueryProvider
	addr     string
	token    string
}

// RuntimeStatusProvider 暴露服务进程内的轻量运行态，供本机 CLI 做重启保护。
type RuntimeStatusProvider interface {
	ActiveTaskCount() int
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

// WithCodexAccountController 配置仅本机可访问的 Codex 账号控制器。
func WithCodexAccountController(controller CodexAccountController) Option {
	return func(s *Server) {
		s.accounts = controller
	}
}

// WithTraceQueryProvider 配置只允许本机查询的结构化诊断 Trace。
func WithTraceQueryProvider(provider observability.QueryProvider) Option {
	return func(s *Server) {
		s.traces = provider
	}
}

// NewServer creates an API server.
func NewServer(clients []*ilink.Client, addr string, options ...Option) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	server := &Server{clients: clients, addr: addr}
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
	mux.HandleFunc("/api/traces", s.handleTraceQuery)
	mux.HandleFunc("/api/codex/accounts", s.handleCodexAccounts)
	mux.HandleFunc("/api/codex/accounts/current", s.handleCodexAccountCurrent)
	mux.HandleFunc("/api/codex/accounts/save", s.handleCodexAccountSave)
	mux.HandleFunc("/api/codex/accounts/use", s.handleCodexAccountUse)
	mux.HandleFunc("/api/codex/accounts/remove", s.handleCodexAccountRemove)
	mux.HandleFunc("/api/codex/accounts/doctor", s.handleCodexAccountDoctor)
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
