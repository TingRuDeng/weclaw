package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestHandleConfigPUTValidatesAndPersistsSoftChange(t *testing.T) {
	current := config.DefaultConfig()
	var saved *config.Config
	server := NewServer(Options{})
	server.cfg = &configService{
		load: func() (*config.Config, error) { return current, nil },
		save: func(next *config.Config) error {
			saved = next
			return nil
		},
	}
	view := redactConfig(current)
	view.RateLimitPerMinute = 42
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleConfig(recorder, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if saved == nil || saved.RateLimitPerMinute != 42 {
		t.Fatalf("saved config=%#v", saved)
	}
	if !strings.Contains(recorder.Body.String(), `"restart_required":false`) {
		t.Fatalf("response=%s, want soft reload result", recorder.Body.String())
	}
}

func TestHandleFeishuCredentialsWritesSecretWithoutEcho(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := NewServer(Options{})
	body := strings.NewReader(`{"name":"project-a","app_id":"cli_a","app_secret":"secret-a"}`)
	recorder := httptest.NewRecorder()
	server.handleFeishuCredentials(recorder, httptest.NewRequest(http.MethodPost, "/api/feishu/credentials", body))

	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "secret-a") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	credentials, err := feishu.LoadCredentialsForBot("project-a")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AppID != "cli_a" || credentials.AppSecret != "secret-a" {
		t.Fatalf("credentials=%#v", credentials)
	}
}

func TestHandleWeChatLoginStartCompletesInjectedFlow(t *testing.T) {
	server := NewServer(Options{})
	store := server.wechatLogins
	store.fetchQR = func(context.Context) (*ilink.QRCodeResponse, error) {
		return &ilink.QRCodeResponse{QRCode: "qr-code", QRCodeImgContent: "qr-content"}, nil
	}
	store.poll = func(_ context.Context, qrCode string, onStatus func(string)) (*ilink.Credentials, error) {
		if qrCode != "qr-code" {
			t.Errorf("qrCode=%q", qrCode)
		}
		onStatus("scanned")
		return &ilink.Credentials{}, nil
	}
	saved := make(chan struct{}, 1)
	store.save = func(*ilink.Credentials) error {
		saved <- struct{}{}
		return nil
	}

	recorder := httptest.NewRecorder()
	server.handleWeChatLoginStart(recorder, httptest.NewRequest(http.MethodPost, "/api/wechat/login/start", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		LoginID string `json:"login_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.LoginID == "" {
		t.Fatalf("response=%s err=%v", recorder.Body.String(), err)
	}
	select {
	case <-saved:
	case <-time.After(time.Second):
		t.Fatal("injected WeChat login did not persist credentials")
	}
	deadline := time.Now().Add(time.Second)
	for store.statusOf(response.LoginID) != "confirmed" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := store.statusOf(response.LoginID); got != "confirmed" {
		t.Fatalf("login status=%q", got)
	}
}
