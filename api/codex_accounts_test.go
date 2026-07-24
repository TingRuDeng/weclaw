package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
)

type fakeCodexAccountController struct {
	status    agent.CodexAccountStatus
	useErr    error
	listCalls int
}

func (f *fakeCodexAccountController) ListCodexAccounts(context.Context) (agent.CodexAccountStatus, error) {
	f.listCalls++
	return f.status, nil
}
func (f *fakeCodexAccountController) CurrentCodexAccount(context.Context, bool) (agent.CodexAccountStatus, error) {
	return f.status, nil
}
func (f *fakeCodexAccountController) SaveCodexAccount(context.Context, agent.CodexAccountSaveOptions) (agent.CodexAccountProfile, error) {
	return agent.CodexAccountProfile{}, nil
}
func (f *fakeCodexAccountController) UseCodexAccount(context.Context, string, uint64) (agent.CodexAccountSwitchResult, error) {
	return agent.CodexAccountSwitchResult{}, f.useErr
}
func (f *fakeCodexAccountController) RemoveCodexAccount(context.Context, string) error { return nil }
func (f *fakeCodexAccountController) DoctorCodexAccounts(context.Context) codexauth.DoctorResult {
	return codexauth.DoctorResult{OK: true, Message: "ok", HostID: "host-safe", Store: "/secret/store", Auth: "/secret/auth.json"}
}

func TestCodexAccountAPIRejectsNonLoopbackSourceEvenWithToken(t *testing.T) {
	controller := &fakeCodexAccountController{}
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"), WithCodexAccountController(controller))
	req := httptest.NewRequest(http.MethodGet, "/api/codex/accounts", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "203.0.113.8:43210"
	req.Header.Set("X-WeClaw-Token", "secret-token")
	rec := httptest.NewRecorder()

	server.handleCodexAccounts(rec, req)
	if rec.Code != http.StatusForbidden || controller.listCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%q", rec.Code, controller.listCalls, rec.Body.String())
	}
}

func TestCodexAccountAPIRequiresConfiguredToken(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"), WithCodexAccountController(&fakeCodexAccountController{}))
	req := httptest.NewRequest(http.MethodGet, "/api/codex/accounts", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()

	server.handleCodexAccounts(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCodexAccountAPIResponseIsRedacted(t *testing.T) {
	profile := agent.CodexAccountProfile{
		ID: "11111111-1111-4111-8111-111111111111", Label: "工作账号",
		AuthMode: "chatgpt", EmailMasked: "a***e@example.com", SecretBackend: codexauth.SecretBackendKeyring,
	}
	controller := &fakeCodexAccountController{status: agent.CodexAccountStatus{
		Store: agent.CodexAccountStoreStatus{Revision: 3, Current: &profile, Profiles: []agent.CodexAccountProfile{profile}},
		Sync: agent.CodexAccountSyncStatus{
			State: agent.CodexAccountSyncSynced, AuthProfile: &profile, LiveProfile: &profile,
		},
	}}
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret-token"), WithCodexAccountController(controller))
	req := httptest.NewRequest(http.MethodGet, "/api/codex/accounts", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("X-WeClaw-Token", "secret-token")
	rec := httptest.NewRecorder()

	server.handleCodexAccounts(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "a***e@example.com") {
		t.Fatalf("status=%d body=%q", rec.Code, body)
	}
	for _, forbidden := range []string{"secret_ref", "access_token", "refresh_token", "alice@example.com"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestCodexAccountAPIBusyErrorIsStructured(t *testing.T) {
	controller := &fakeCodexAccountController{useErr: codexauth.NewError(codexauth.CodeBusy, "当前有任务", errors.New("private detail"))}
	server := NewServer(nil, "127.0.0.1:18011", WithCodexAccountController(controller))
	req := httptest.NewRequest(http.MethodPost, "/api/codex/accounts/use", strings.NewReader(`{"profile":"work"}`))
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()

	server.handleCodexAccountUse(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"codex_account_busy"`) || strings.Contains(rec.Body.String(), "private detail") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCodexAccountDoctorOmitsMachinePaths(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithCodexAccountController(&fakeCodexAccountController{}))
	req := httptest.NewRequest(http.MethodGet, "/api/codex/accounts/doctor", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()

	server.handleCodexAccountDoctor(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "/secret/") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
