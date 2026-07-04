package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

const maxSendRequestBytes = 1 * 1024 * 1024

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

// SendRequest is the JSON body for POST /api/send.
type SendRequest struct {
	Platform  string `json:"platform,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	To        string `json:"to"`
	Text      string `json:"text,omitempty"`
	MediaURL  string `json:"media_url,omitempty"` // image/video/file URL
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

	srv := &http.Server{Addr: s.addr, Handler: mux}

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

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeSend(w, r) {
		return
	}

	req, ok := decodeSendRequest(w, r)
	if !ok {
		return
	}
	reply, ok := s.replierFor(w, req)
	if !ok {
		return
	}
	if err := s.sendRequest(r.Context(), reply, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func decodeSendRequest(w http.ResponseWriter, r *http.Request) (SendRequest, bool) {
	var req SendRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSendRequestBytes))
	if err := decoder.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return SendRequest{}, false
		}
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return SendRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid JSON: multiple JSON values", http.StatusBadRequest)
		return SendRequest{}, false
	}
	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return SendRequest{}, false
	}
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return SendRequest{}, false
	}
	return req, true
}

func (s *Server) replierFor(w http.ResponseWriter, req SendRequest) (platform.Replier, bool) {
	if s.registry != nil {
		platformName := platform.PlatformName(strings.TrimSpace(req.Platform))
		if platformName == "" {
			platformName = platform.PlatformWeChat
		}
		reply, ok := s.registry.ReplierFor(platformName, strings.TrimSpace(req.AccountID), req.To)
		if !ok {
			http.Error(w, "target platform or account not configured", http.StatusServiceUnavailable)
			return nil, false
		}
		return reply, true
	}
	if len(s.clients) == 0 {
		http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
		return nil, false
	}
	return wechat.NewReplier(s.clients[0], req.To, "", ""), true
}

func (s *Server) sendRequest(ctx context.Context, reply platform.Replier, req SendRequest) error {
	if req.Text != "" {
		if err := reply.SendText(ctx, req.Text); err != nil {
			log.Printf("[api] send text failed: %v", err)
			return fmt.Errorf("send text failed: %w", err)
		}
		log.Printf("[api] sent text to %s: %q", req.To, req.Text)
		s.sendExtractedImages(ctx, reply, req)
	}

	if req.MediaURL != "" {
		remoteReply, ok := reply.(interface {
			SendMediaFromURL(ctx context.Context, rawURL string) error
		})
		if !ok {
			return fmt.Errorf("target platform does not support remote media URL sending")
		}
		if err := remoteReply.SendMediaFromURL(ctx, req.MediaURL); err != nil {
			log.Printf("[api] send media failed: %v", err)
			return fmt.Errorf("send media failed: %w", err)
		}
		log.Printf("[api] sent media to %s: %s", req.To, req.MediaURL)
	}
	return nil
}

func (s *Server) sendExtractedImages(ctx context.Context, reply platform.Replier, req SendRequest) {
	remoteReply, ok := reply.(interface {
		SendMediaFromURL(ctx context.Context, rawURL string) error
	})
	if !ok {
		return
	}
	for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
		if err := remoteReply.SendMediaFromURL(ctx, imgURL); err != nil {
			log.Printf("[api] send extracted image failed: %v", err)
		} else {
			log.Printf("[api] sent extracted image to %s: %s", req.To, imgURL)
		}
	}
}

func (s *Server) authorizeSend(w http.ResponseWriter, r *http.Request) bool {
	return s.authorizeRead(w, r)
}

func (s *Server) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		return true
	}
	if constantTimeEqual(sendAuthToken(r), s.token) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func sendAuthToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-WeClaw-Token")); token != "" {
		return token
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return ""
	}
	fields := strings.Fields(auth)
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return strings.TrimSpace(fields[1])
	}
	return ""
}

func constantTimeEqual(got string, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isLoopbackListenAddr(addr string) bool {
	host := listenHost(addr)
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func listenHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}
