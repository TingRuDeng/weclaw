package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/internal/auththrottle"
	"github.com/fastclaw-ai/weclaw/messaging"
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

func TestHandleSendRateLimitsFailedAuthenticationByRemoteAddr(t *testing.T) {
	server := NewServer(nil, "0.0.0.0:18011", WithToken("secret-token"))

	for i := 0; i < auththrottle.DefaultMaxFailures; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"to":"u","text":"hi"}`))
		req.RemoteAddr = "203.0.113.7:4000"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		rec := httptest.NewRecorder()
		server.handleSend(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status=%d, want 401", i+1, rec.Code)
		}
	}

	blockedReq := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"to":"u","text":"hi"}`))
	blockedReq.RemoteAddr = "203.0.113.7:4999"
	blockedReq.Header.Set("Authorization", "Bearer secret-token")
	blockedRec := httptest.NewRecorder()
	server.handleSend(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status=%d, want 429", blockedRec.Code)
	}

	otherReq := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"to":"u","text":"hi"}`))
	otherReq.RemoteAddr = "203.0.113.8:4000"
	otherReq.Header.Set("Authorization", "Bearer secret-token")
	otherRec := httptest.NewRecorder()
	server.handleSend(otherRec, otherReq)
	if otherRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("other source status=%d, want authenticated 503", otherRec.Code)
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

func TestHandleRuntimeDrainStartsAndCancelsAuthenticatedDrain(t *testing.T) {
	control := &staticRuntimeControl{result: messaging.RuntimeDrainResult{ActiveTasks: 1, RemainingTasks: 0}}
	server := NewServer(nil, "127.0.0.1:18011", WithRuntimeDrainController(control))

	start := httptest.NewRequest(http.MethodPost, "/api/runtime/drain?force=true", nil)
	start.Host = "127.0.0.1:18011"
	start.RemoteAddr = "127.0.0.1:40001"
	startRec := httptest.NewRecorder()
	server.handleRuntimeDrain(startRec, start)
	if startRec.Code != http.StatusOK || !control.force {
		t.Fatalf("start status=%d force=%v body=%q", startRec.Code, control.force, startRec.Body.String())
	}

	cancel := httptest.NewRequest(http.MethodDelete, "/api/runtime/drain", nil)
	cancel.Host = "127.0.0.1:18011"
	cancel.RemoteAddr = "127.0.0.1:40001"
	cancelRec := httptest.NewRecorder()
	server.handleRuntimeDrain(cancelRec, cancel)
	if cancelRec.Code != http.StatusOK || !control.cancelled {
		t.Fatalf("cancel status=%d cancelled=%v body=%q", cancelRec.Code, control.cancelled, cancelRec.Body.String())
	}
}

func TestHandleRuntimeDrainReportsActiveTaskConflict(t *testing.T) {
	control := &staticRuntimeControl{
		result: messaging.RuntimeDrainResult{ActiveTasks: 2, RemainingTasks: 2},
		err:    messaging.ErrActiveTasksRunning,
	}
	server := NewServer(nil, "127.0.0.1:18011", WithRuntimeDrainController(control))
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/drain", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:40001"
	rec := httptest.NewRecorder()
	server.handleRuntimeDrain(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"active_tasks":2`) {
		t.Fatalf("status=%d body=%q, want active-task conflict", rec.Code, rec.Body.String())
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

func TestHandleSendReportsPartialSuccessWhenMediaFailsAfterText(t *testing.T) {
	reply := &recordingReplier{mediaErr: fmt.Errorf("remote media unavailable")}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboundPlatform{name: platform.PlatformFeishu, account: "cli_a", reply: reply},
		Access:   platform.NewAccessControl([]string{"ignored"}),
	}})
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(registry))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(
		`{"platform":"feishu","account_id":"cli_a","to":"ou_user","text":"hi","media_url":"https://example.com/image.png"}`,
	))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status=%d, body=%q, want %d", rec.Code, rec.Body.String(), http.StatusMultiStatus)
	}
	var response struct {
		Status    string `json:"status"`
		TextSent  bool   `json:"text_sent"`
		MediaSent bool   `json:"media_sent"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "partial" || !response.TextSent || response.MediaSent {
		t.Fatalf("response=%+v, want partial text-only success", response)
	}
	if len(reply.texts) != 1 || len(reply.mediaURLs) != 1 {
		t.Fatalf("reply=%#v, want one text and one media attempt", reply)
	}
}

func TestHandleSendReportsPartialSuccessWhenExtractedImageFailsAfterText(t *testing.T) {
	reply := &recordingReplier{mediaErr: fmt.Errorf("remote media unavailable")}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboundPlatform{name: platform.PlatformFeishu, account: "cli_a", reply: reply},
		Access:   platform.NewAccessControl([]string{"ignored"}),
	}})
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(registry))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(
		`{"platform":"feishu","account_id":"cli_a","to":"ou_user","text":"hi ![image](https://example.com/image.png)"}`,
	))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status=%d, body=%q, want %d", rec.Code, rec.Body.String(), http.StatusMultiStatus)
	}
	var response partialSendResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "partial" || !response.TextSent || response.MediaSent {
		t.Fatalf("response=%+v, want partial text-only success", response)
	}
	if len(reply.texts) != 1 || len(reply.mediaURLs) != 1 {
		t.Fatalf("reply=%#v, want one text and one extracted media attempt", reply)
	}
}

func TestHandleSendReportsSuccessfulMediaWhenOnlySomeExtractedImagesFail(t *testing.T) {
	reply := &recordingReplier{mediaErrs: []error{nil, fmt.Errorf("second image unavailable")}}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboundPlatform{name: platform.PlatformFeishu, account: "cli_a", reply: reply},
		Access:   platform.NewAccessControl([]string{"ignored"}),
	}})
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(registry))

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(
		`{"platform":"feishu","account_id":"cli_a","to":"ou_user","text":"![one](https://example.com/one.png) ![two](https://example.com/two.png)"}`,
	))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status=%d, body=%q, want %d", rec.Code, rec.Body.String(), http.StatusMultiStatus)
	}
	var response partialSendResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "partial" || !response.TextSent || !response.MediaSent {
		t.Fatalf("response=%+v, want partial text and media success", response)
	}
	if len(reply.mediaURLs) != 2 {
		t.Fatalf("media attempts=%d, want 2", len(reply.mediaURLs))
	}
}

func TestSendRequestPreflightsExtractedImageCapability(t *testing.T) {
	reply := &textOnlyRecordingReplier{}
	result, err := (&Server{}).sendRequest(context.Background(), reply, SendRequest{
		To:   "ou_user",
		Text: "hi ![image](https://example.com/image.png)",
	})

	if err == nil {
		t.Fatal("sendRequest error=nil, want unsupported media error")
	}
	if result.textSent || len(reply.texts) != 0 {
		t.Fatalf("result=%+v texts=%#v, media capability must be checked before sending text", result, reply.texts)
	}
}

func TestHandleSendLogDoesNotContainMessageBody(t *testing.T) {
	reply := &recordingReplier{}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &outboundPlatform{name: platform.PlatformFeishu, account: "cli_a", reply: reply},
		Access:   platform.NewAccessControl([]string{"ignored"}),
	}})
	server := NewServer(nil, "127.0.0.1:18011", WithRegistry(registry))
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"platform":"feishu","account_id":"cli_a","to":"ou_user","text":"top-secret-message"}`))
	req.Host = "127.0.0.1:18011"
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, body=%q, want 200", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), "top-secret-message") {
		t.Fatalf("API log contains message body: %q", logs.String())
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

type staticRuntimeControl struct {
	result    messaging.RuntimeDrainResult
	err       error
	force     bool
	cancelled bool
}

func (s *staticRuntimeControl) Drain(_ context.Context, force bool) (messaging.RuntimeDrainResult, error) {
	s.force = force
	return s.result, s.err
}

func (s *staticRuntimeControl) CancelDrain() {
	s.cancelled = true
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
	to        string
	texts     []string
	mediaURLs []string
	mediaErr  error
	mediaErrs []error
}

type textOnlyRecordingReplier struct {
	texts []string
}

func (r *textOnlyRecordingReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}

func (r *textOnlyRecordingReplier) SendText(ctx context.Context, text string) error {
	r.texts = append(r.texts, text)
	return nil
}

func (r *textOnlyRecordingReplier) SendImage(ctx context.Context, localPath string) error {
	return platform.ErrUnsupported
}

func (r *textOnlyRecordingReplier) SendFile(ctx context.Context, localPath string) error {
	return platform.ErrUnsupported
}

func (r *textOnlyRecordingReplier) Typing(ctx context.Context, on bool) error {
	return nil
}

func (r *textOnlyRecordingReplier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return nil, platform.ErrUnsupported
}

func (r *textOnlyRecordingReplier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	return platform.ErrUnsupported
}

func (r *recordingReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}

func (r *recordingReplier) SendText(ctx context.Context, text string) error {
	r.texts = append(r.texts, text)
	return nil
}

func (r *recordingReplier) SendMediaFromURL(ctx context.Context, mediaURL string) error {
	attempt := len(r.mediaURLs)
	r.mediaURLs = append(r.mediaURLs, mediaURL)
	if attempt < len(r.mediaErrs) {
		return r.mediaErrs[attempt]
	}
	return r.mediaErr
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
