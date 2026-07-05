package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

func boolPtr(b bool) *bool { return &b }

func TestRedactConfigHidesSecrets(t *testing.T) {
	cfg := &config.Config{
		APIToken: "super-secret-token",
		Agents: map[string]config.AgentConfig{
			"claude": {Type: "cli", Command: "claude", APIKey: "sk-ant-xxx", Env: map[string]string{"ANTHROPIC_API_KEY": "sk-zzz"}},
		},
	}
	view := redactConfig(cfg)
	blob, _ := json.Marshal(view)
	s := string(blob)
	for _, secret := range []string{"super-secret-token", "sk-ant-xxx", "sk-zzz"} {
		if strings.Contains(s, secret) {
			t.Fatalf("redacted view leaked secret %q: %s", secret, s)
		}
	}
	if view.APIToken != secretMask || view.Agents["claude"].APIKey != secretMask {
		t.Fatal("non-empty secrets should be masked")
	}
	if view.Agents["claude"].Env["ANTHROPIC_API_KEY"] != secretMask {
		t.Fatal("env values should be masked, keys preserved")
	}
}

func TestMergeViewPreservesMaskedSecrets(t *testing.T) {
	current := &config.Config{
		APIToken: "keep-token",
		Agents: map[string]config.AgentConfig{
			"claude": {Type: "cli", Command: "claude", APIKey: "keep-key", Env: map[string]string{"K": "keep-val"}},
		},
	}
	view := redactConfig(current)
	// 模拟前端只改了 command，密钥仍是掩码
	a := view.Agents["claude"]
	a.Command = "claude-2"
	view.Agents["claude"] = a

	merged := mergeView(current, view)
	if merged.APIToken != "keep-token" {
		t.Fatalf("masked api_token must be preserved, got %q", merged.APIToken)
	}
	ag := merged.Agents["claude"]
	if ag.APIKey != "keep-key" || ag.Env["K"] != "keep-val" {
		t.Fatalf("masked secrets must be preserved: %+v", ag)
	}
	if ag.Command != "claude-2" {
		t.Fatalf("non-secret change should apply, got %q", ag.Command)
	}
}

func TestMergeViewOverwritesNewSecret(t *testing.T) {
	current := &config.Config{APIToken: "old"}
	view := redactConfig(current)
	view.APIToken = "new-token" // 用户输入新明文
	merged := mergeView(current, view)
	if merged.APIToken != "new-token" {
		t.Fatalf("new secret should overwrite, got %q", merged.APIToken)
	}
}

func TestMergeViewUpdatesAdminUsers(t *testing.T) {
	current := &config.Config{AdminUsers: []string{"old_admin"}}
	view := redactConfig(current)
	view.AdminUsers = []string{"new_admin"}

	merged := mergeView(current, view)

	if !reflect.DeepEqual(merged.AdminUsers, []string{"new_admin"}) {
		t.Fatalf("AdminUsers=%#v, want new_admin", merged.AdminUsers)
	}
}

func TestPlatformTopologyChanged(t *testing.T) {
	cur := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(false)}}}
	soft := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(false), AllowedUsers: []string{"u1"}}}}
	if platformTopologyChanged(cur, soft) {
		t.Fatal("allowed_users change is soft, must not require restart")
	}
	topo := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(true)}}}
	if !platformTopologyChanged(cur, topo) {
		t.Fatal("enabling a platform must require restart")
	}
}

func TestValidateConfigRejectsBadAgent(t *testing.T) {
	if err := validateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {Type: "cli"}}}); err == nil {
		t.Fatal("cli agent without command should fail validation")
	}
	if err := validateConfig(&config.Config{Agents: map[string]config.AgentConfig{"x": {Type: "http"}}}); err == nil {
		t.Fatal("http agent without endpoint should fail validation")
	}
	if err := validateConfig(&config.Config{RateLimitPerMinute: -1}); err == nil {
		t.Fatal("negative rate limit should fail validation")
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
