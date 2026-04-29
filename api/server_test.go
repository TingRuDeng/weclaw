package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
