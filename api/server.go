package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
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
	addr     string
	token    string
}

// RuntimeStatusProvider 暴露服务进程内的轻量运行态，供本机 CLI 做重启保护。
type RuntimeStatusProvider interface {
	ActiveTaskCount() int
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"active_tasks": activeTasks,
	})
}
