package web

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFrontendDoesNotRenderServerDataWithInnerHTML(t *testing.T) {
	data, err := fs.ReadFile(staticFS, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "innerHTML") {
		t.Fatal("app.js must use DOM text nodes instead of innerHTML")
	}
}

func TestFrontendShowsClaudeACPAndLocalHandoffState(t *testing.T) {
	data, err := fs.ReadFile(staticFS, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "a.local_command") || !strings.Contains(text, "本地交接") {
		t.Fatal("配置面板未展示 Claude ACP 本地交接状态")
	}
}

func TestWorkspaceRootsHintMatchesRuntimeAccessRules(t *testing.T) {
	data, err := fs.ReadFile(staticFS, "static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"普通用户允许的工作目录根",
		"未配置时，普通用户远程 /cwd 被禁用",
		"管理员不受此限制",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("工作空间提示缺少 %q", want)
		}
	}
	if strings.Contains(text, "未配置工作目录根时，/cwd 可指向任意目录") {
		t.Fatal("配置面板仍展示与运行时相反的旧提示")
	}
}

func TestWebHTTPServerHasSlowClientTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("timeouts=%+v, want all server timeouts configured", srv)
	}
}

func TestWebDataDirUsesWECLAWHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	if got := webDataDir(); got != home {
		t.Fatalf("webDataDir=%q, want %q", got, home)
	}
}

func TestProcessSignalMeansRunningOnEPERM(t *testing.T) {
	if !processSignalMeansRunning(syscall.EPERM) || processSignalMeansRunning(errors.New("missing")) {
		t.Fatal("EPERM should mean running and unrelated errors should not")
	}
}

func TestParseRuntimePIDSupportsJSONAndLegacyFormat(t *testing.T) {
	if got := parseRuntimePID([]byte(`{"pid":123,"mode":"background"}`)); got != 123 {
		t.Fatalf("json pid=%d, want 123", got)
	}
	if got := parseRuntimePID([]byte("456\n")); got != 456 {
		t.Fatalf("legacy pid=%d, want 456", got)
	}
}

func TestValidateRequiresTokenForNonLoopback(t *testing.T) {
	if err := NewServer(Options{Addr: "0.0.0.0:39282"}).Validate(); err == nil {
		t.Fatal("non-loopback without token must fail Validate")
	}
	if err := NewServer(Options{Addr: "127.0.0.1:39282"}).Validate(); err != nil {
		t.Fatalf("loopback should be allowed: %v", err)
	}
	if err := NewServer(Options{Addr: "0.0.0.0:39282", Token: "t"}).Validate(); err != nil {
		t.Fatalf("non-loopback with token should be allowed: %v", err)
	}
}

func TestAuthMiddleware(t *testing.T) {
	s := NewServer(Options{Addr: "127.0.0.1:39282", Token: "secret"})
	mux := http.NewServeMux()
	s.routes(mux)
	h := s.guard(mux)

	// 无 token → 401
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401 got %d", rec.Code)
	}
	// 跨站 Origin → 403
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("X-WeClaw-Token", "secret")
	req.Header.Set("Origin", "http://evil.example.com")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin: want 403 got %d", rec.Code)
	}
}

func TestWeChatLoginSessionIsolation(t *testing.T) {
	store := newWechatLoginStore()
	id := store.begin("qr-content-1")
	if store.statusOf(id) != "waiting" {
		t.Fatal("new session should be waiting")
	}
	if store.statusOf("bogus-id") != "expired" {
		t.Fatal("unknown login_id must not leak; expected expired")
	}
	if store.qrContentOf("bogus-id") != "" {
		t.Fatal("unknown login_id must not leak qr content")
	}
	// 过期清理
	store.now = func() time.Time { return time.Now().Add(wechatLoginTTL + time.Minute) }
	if store.statusOf(id) != "expired" {
		t.Fatal("expired session should report expired")
	}
}
