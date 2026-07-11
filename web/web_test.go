package web

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
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
			"claude": {
				Type:             "cli",
				Command:          "claude",
				APIKey:           "keep-key",
				Env:              map[string]string{"K": "keep-val"},
				PermissionLevel:  "auto_review",
				ApprovalPolicy:   "on-request",
				ApprovalReviewer: "auto_review",
				SandboxMode:      "workspace-write",
			},
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
	if ag.PermissionLevel != "auto_review" || ag.ApprovalPolicy != "on-request" ||
		ag.ApprovalReviewer != "auto_review" || ag.SandboxMode != "workspace-write" {
		t.Fatalf("codex permission fields must be preserved: %+v", ag)
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
	if restartRequiredConfigChanged(cur, soft) {
		t.Fatal("allowed_users change is soft, must not require restart")
	}
	topo := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {Enabled: boolPtr(true)}}}
	if !restartRequiredConfigChanged(cur, topo) {
		t.Fatal("enabling a platform must require restart")
	}
}

func TestPlatformTopologyChangedDetectsFeishuBotList(t *testing.T) {
	cur := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {
		Enabled: boolPtr(true),
		Bots:    []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_a"}}},
	}}}
	soft := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {
		Enabled: boolPtr(true),
		Bots:    []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_b"}}},
	}}}
	topo := &config.Config{Platforms: map[string]config.PlatformConfig{"feishu": {
		Enabled: boolPtr(true),
		Bots:    []config.FeishuBotConfig{{Name: "project-a", AppID: "cli_b"}},
	}}}

	if restartRequiredConfigChanged(cur, soft) {
		t.Fatal("allowed_users-only bot change is soft")
	}
	if !restartRequiredConfigChanged(cur, topo) {
		t.Fatal("bot app_id change must require restart")
	}
}

func TestPlatformTopologyChangedDetectsNonReloadableConfig(t *testing.T) {
	base := config.DefaultConfig()
	base.APIAddr = "127.0.0.1:18011"
	base.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex", Model: "gpt-old"}

	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "api address", mutate: func(cfg *config.Config) { cfg.APIAddr = "127.0.0.1:19011" }},
		{name: "save dir", mutate: func(cfg *config.Config) { cfg.SaveDir = "/tmp/output" }},
		{name: "audit path", mutate: func(cfg *config.Config) { cfg.AuditLogPath = "/tmp/audit.log" }},
		{name: "agent model", mutate: func(cfg *config.Config) {
			agentCfg := cfg.Agents["codex"]
			agentCfg.Model = "gpt-new"
			cfg.Agents["codex"] = agentCfg
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := mergeView(base, redactConfig(base))
			tt.mutate(next)
			if !restartRequiredConfigChanged(base, next) {
				t.Fatal("non-reloadable change must require restart")
			}
		})
	}
}

func TestPlatformTopologyChangedIgnoresSoftAgentProgress(t *testing.T) {
	base := config.DefaultConfig()
	base.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	next := mergeView(base, redactConfig(base))
	agentCfg := next.Agents["codex"]
	progress := config.DefaultProgressConfig()
	agentCfg.Progress = &progress
	next.Agents["codex"] = agentCfg

	if restartRequiredConfigChanged(base, next) {
		t.Fatal("agent progress change is soft, must not require restart")
	}
}

func TestPlatformStatusesIncludeEachFeishuBot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := feishu.SaveCredentialsForBot("project-a", feishu.Credentials{AppID: "cli_a", AppSecret: "secret-a"}); err != nil {
		t.Fatalf("SaveCredentialsForBot error: %v", err)
	}
	enabled := true
	cfg := config.DefaultConfig()
	cfg.Platforms["feishu"] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "project-a", AppID: "cli_a", AllowedUsers: []string{"ou_a"}},
			{Name: "project-b", AppID: "cli_b"},
		},
	}

	statuses := platformStatuses(cfg)

	first, ok := findPlatformStatus(statuses, "feishu/project-a")
	if !ok {
		t.Fatalf("missing feishu/project-a status: %#v", statuses)
	}
	if !first.CredentialsPresent || first.AllowedUsersCount != 1 {
		t.Fatalf("project-a status=%#v, want credentials and one allowed user", first)
	}
	second, ok := findPlatformStatus(statuses, "feishu/project-b")
	if !ok {
		t.Fatalf("missing feishu/project-b status: %#v", statuses)
	}
	if second.CredentialsPresent || second.AllowedUsersCount != 0 {
		t.Fatalf("project-b status=%#v, want no credentials and empty allowlist", second)
	}
}

func findPlatformStatus(statuses []platformStatus, name string) (platformStatus, bool) {
	for _, status := range statuses {
		if status.Name == name {
			return status, true
		}
	}
	return platformStatus{}, false
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
