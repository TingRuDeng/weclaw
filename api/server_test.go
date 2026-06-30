package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

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
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q, want 200", rec.Code, rec.Body.String())
	}
	if reply.to != "ou_user" || len(reply.texts) != 1 || reply.texts[0] != "hi" {
		t.Fatalf("reply=%#v, want feishu target text", reply)
	}
}

func TestHandleSendReturnsUnavailableForMissingRegistryTarget(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(platform.NewRegistry(nil)))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"platform":"feishu","to":"ou_user","text":"hi"}`))
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
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
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
