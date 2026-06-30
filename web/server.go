package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

//go:embed static/*
var staticFS embed.FS

// Options 配置 web 面板服务。
type Options struct {
	Addr  string
	Token string
}

// Server 提供本机配置面板的 HTTP 服务。
type Server struct {
	addr         string
	token        string
	cfg          *configService
	wechatLogins *wechatLoginStore
	authThrottle *authThrottle
}

// NewServer 创建配置面板服务。
func NewServer(opts Options) *Server {
	addr := strings.TrimSpace(opts.Addr)
	if addr == "" {
		addr = "127.0.0.1:39282"
	}
	return &Server{
		addr:         addr,
		token:        strings.TrimSpace(opts.Token),
		cfg:          newConfigService(),
		wechatLogins: newWechatLoginStore(),
		authThrottle: newAuthThrottle(),
	}
}

// Addr 返回监听地址。
func (s *Server) Addr() string { return s.addr }

// Validate 在监听暴露到非回环地址前要求显式 token。
func (s *Server) Validate() error {
	if s.token != "" || isLoopbackListenAddr(s.addr) {
		return nil
	}
	return fmt.Errorf("web panel token is required when addr %q is not loopback", s.addr)
}

// Run 启动 HTTP 服务，阻塞直到 ctx 取消。
func (s *Server) Run(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	s.routes(mux)

	srv := &http.Server{Addr: s.addr, Handler: s.guard(mux)}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) routes(mux *http.ServeMux) {
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/", s.handleIndex(sub))

	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/feishu/credentials", s.handleFeishuCredentials)
	mux.HandleFunc("/api/validate/feishu", s.handleValidateFeishu)
	mux.HandleFunc("/api/wechat/login/start", s.handleWeChatLoginStart)
	mux.HandleFunc("/api/wechat/login/status", s.handleWeChatLoginStatus)
	mux.HandleFunc("/api/wechat/login/qr", s.handleWeChatLoginQR)
}

// guard 应用同源防护 + 常量时间 token 校验；静态资源与首页放行 token 但仍做同源。
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			client := clientKey(r)
			if s.authThrottle.blocked(client) {
				http.Error(w, "too many failed auth attempts, slow down", http.StatusTooManyRequests)
				return
			}
			if !s.authorized(r) {
				s.authThrottle.fail(client)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			s.authThrottle.reset(client)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	return constantTimeEqual(tokenFromRequest(r), s.token)
}

// sameOrigin 拒绝来自其它源的请求，防 DNS rebinding / CSRF。无 Origin/Referer 的同机直连放行。
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		return true // 非浏览器跨站场景(如本地 curl)；token 仍是主防线
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func tokenFromRequest(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-WeClaw-Token")); t != "" {
		return t
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if fields := strings.Fields(auth); len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return strings.TrimSpace(fields[1])
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func constantTimeEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isLoopbackListenAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
