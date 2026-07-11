package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestAPIHTTPServerHasSlowClientTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("timeouts=%+v, want all server timeouts configured", srv)
	}
}

func TestHandleSendRequiresConfiguredToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"to":"u","text":"hi"}`))
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleSendAcceptsBearerToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"to":"u","text":"hi"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleSendAcceptsHeaderToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"to":"u","text":"hi"}`))
	req.Header.Set("X-WeClaw-Token", "secret-token")
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleRuntimeStatusReturnsActiveTaskCount(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithRuntimeStatusProvider(staticRuntimeStatus{active: 2}))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime", nil)
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleRuntimeStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Status      string `json:"status"`
		ActiveTasks int    `json:"active_tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse runtime status: %v", err)
	}
	if body.Status != "ok" || body.ActiveTasks != 2 {
		t.Fatalf("runtime status=%#v, want ok with 2 active tasks", body)
	}
}

func TestHandleRuntimeStatusRequiresTokenWhenConfigured(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime", nil)
	rec := httptest.NewRecorder()
	server.handleRuntimeStatus(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthorizeReadRejectsExternalHostWithoutToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011")
	req := httptest.NewRequest(http.MethodGet, "/api/runtime", nil)
	req.Host = "attacker.example:18011"
	rec := httptest.NewRecorder()

	if server.authorizeRead(rec, req) {
		t.Fatal("authorizeRead accepted external Host without token")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthorizeReadRejectsCrossOriginWithoutToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011")
	req := httptest.NewRequest(http.MethodPost, "/api/send", nil)
	req.Host = "127.0.0.1:18011"
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	rec := httptest.NewRecorder()

	if server.authorizeRead(rec, req) {
		t.Fatal("authorizeRead accepted cross-origin request without token")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthorizeReadAllowsLoopbackHostWithoutToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011")
	req := httptest.NewRequest(http.MethodGet, "/api/runtime", nil)
	req.Host = "localhost:18011"
	rec := httptest.NewRecorder()

	if !server.authorizeRead(rec, req) {
		t.Fatalf("authorizeRead rejected loopback Host, status=%d", rec.Code)
	}
}

func TestHandleSendUsesRegistryTarget(t *testing.T) {
	reply := &recordingReplier{}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboundPlatform{
			name:    platform.PlatformFeishu,
			account: "cli_a",
			reply:   reply,
		},
		Access: platform.NewAccessControl([]string{"ignored"}),
	}})
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(registry))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"platform":"feishu","account_id":"cli_a","to":"ou_user","text":"hi"}`))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q, want 200", rec.Code, rec.Body.String())
	}
	if reply.to != "ou_user" || len(reply.texts) != 1 || reply.texts[0] != "hi" {
		t.Fatalf("reply=%#v, want feishu target text", reply)
	}
}

func TestHandleSendRequiresAccountIDWhenPlatformHasMultipleAccounts(t *testing.T) {
	first := &recordingReplier{}
	second := &recordingReplier{}
	registry := platform.NewRegistry([]platform.RegistryEntry{
		{
			Platform: &outboundPlatform{name: platform.PlatformFeishu, account: "cli_a", reply: first},
			Access:   platform.NewAccessControl([]string{"ignored"}),
		},
		{
			Platform: &outboundPlatform{name: platform.PlatformFeishu, account: "cli_b", reply: second},
			Access:   platform.NewAccessControl([]string{"ignored"}),
		},
	})
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(registry))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"platform":"feishu","to":"ou_user","text":"hi"}`))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%q, want 400", rec.Code, rec.Body.String())
	}
	if len(first.texts) != 0 || len(second.texts) != 0 {
		t.Fatalf("ambiguous send should not use any bot, first=%#v second=%#v", first.texts, second.texts)
	}
}

func TestHandleSendReturnsUnavailableForMissingRegistryTarget(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(platform.NewRegistry(nil)))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"platform":"feishu","to":"ou_user","text":"hi"}`))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleSendRejectsOversizedBody(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011")
	body := `{"to":"u","text":"` + strings.Repeat("x", maxSendRequestBytes) + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(body))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

type staticRuntimeStatus struct {
	active int
}

func (s staticRuntimeStatus) ActiveTaskCount() int {
	return s.active
}

type outboundPlatform struct {
	name    platform.PlatformName
	account string
	reply   *recordingReplier
}

func (p *outboundPlatform) Name() platform.PlatformName {
	return p.name
}

func (p *outboundPlatform) AccountID() string {
	return p.account
}

func (p *outboundPlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}

func (p *outboundPlatform) Run(ctx context.Context, dispatch platform.DispatchFunc) error {
	return nil
}

// NewReplier 记录 API 选择的目标会话。
func (p *outboundPlatform) NewReplier(chatID string) platform.Replier {
	p.reply.to = chatID
	return p.reply
}

type recordingReplier struct {
	to    string
	texts []string
}

func (r *recordingReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}

func (r *recordingReplier) SendText(ctx context.Context, text string) error {
	r.texts = append(r.texts, text)
	return nil
}

func (r *recordingReplier) SendImage(ctx context.Context, localPath string) error {
	return nil
}

func (r *recordingReplier) SendFile(ctx context.Context, localPath string) error {
	return nil
}

func (r *recordingReplier) Typing(ctx context.Context, on bool) error {
	return nil
}

func (r *recordingReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return nil, platform.ErrUnsupported
}

func (r *recordingReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	return platform.ErrUnsupported
}

func TestValidateRejectsNonLoopbackListenWithoutToken(t *testing.T) {
	server := NewServer(nil, "0.0.0.0:18011")

	if err := server.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want non-loopback rejection")
	}
}

func TestValidateAllowsLoopbackListenWithoutToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011")

	if err := server.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateAllowsNonLoopbackListenWithToken(t *testing.T) {
	server := NewServer(nil, "0.0.0.0:18011", WithToken("secret-token"))

	if err := server.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
