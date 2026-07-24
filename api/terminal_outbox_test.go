package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/messaging"
)

type terminalOutboxControllerStub struct {
	status      messaging.TerminalOutboxStatus
	redriveID   string
	redriveErr  error
	statusCalls int
}

func (stub *terminalOutboxControllerStub) TerminalOutboxStatus(context.Context) (messaging.TerminalOutboxStatus, error) {
	stub.statusCalls++
	return stub.status, nil
}

func (stub *terminalOutboxControllerStub) RedriveTerminalOutbox(_ context.Context, id string) (messaging.TerminalOutboxRedriveResult, error) {
	stub.redriveID = id
	return messaging.TerminalOutboxRedriveResult{Requested: 1, Status: stub.status}, stub.redriveErr
}

func TestTerminalOutboxAPIRequiresLoopbackAndToken(t *testing.T) {
	stub := &terminalOutboxControllerStub{}
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret"), WithTerminalOutboxController(stub))
	remote := httptest.NewRequest(http.MethodGet, "/api/terminal-outbox", nil)
	remote.Host = "127.0.0.1:18011"
	remote.RemoteAddr = "203.0.113.8:43210"
	remote.Header.Set("X-WeClaw-Token", "secret")
	remoteRec := httptest.NewRecorder()
	server.handleTerminalOutboxStatus(remoteRec, remote)
	if remoteRec.Code != http.StatusForbidden || stub.statusCalls != 0 {
		t.Fatalf("remote status=%d calls=%d", remoteRec.Code, stub.statusCalls)
	}

	local := httptest.NewRequest(http.MethodGet, "/api/terminal-outbox", nil)
	local.Host = "127.0.0.1:18011"
	local.RemoteAddr = "127.0.0.1:43210"
	localRec := httptest.NewRecorder()
	server.handleTerminalOutboxStatus(localRec, local)
	if localRec.Code != http.StatusUnauthorized || stub.statusCalls != 0 {
		t.Fatalf("local status=%d calls=%d", localRec.Code, stub.statusCalls)
	}
}

func TestTerminalOutboxAPIRedriveMapsIDAndNotFound(t *testing.T) {
	stub := &terminalOutboxControllerStub{
		status: messaging.TerminalOutboxStatus{Pending: 1},
	}
	server := NewServer(nil, "127.0.0.1:18011", WithTerminalOutboxController(stub))
	req := httptest.NewRequest(http.MethodPost, "/api/terminal-outbox/redrive", strings.NewReader(`{"id":"entry-1"}`))
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	server.handleTerminalOutboxRedrive(rec, req)
	if rec.Code != http.StatusOK || stub.redriveID != "entry-1" || !strings.Contains(rec.Body.String(), `"requested":1`) {
		t.Fatalf("status=%d id=%q body=%q", rec.Code, stub.redriveID, rec.Body.String())
	}

	stub.redriveErr = messaging.ErrTerminalOutboxNotFound
	missing := httptest.NewRequest(http.MethodPost, "/api/terminal-outbox/redrive", strings.NewReader(`{"id":"missing"}`))
	missing.Host = "127.0.0.1:18011"
	missing.RemoteAddr = "127.0.0.1:43210"
	missingRec := httptest.NewRecorder()
	server.handleTerminalOutboxRedrive(missingRec, missing)
	if missingRec.Code != http.StatusNotFound || !strings.Contains(missingRec.Body.String(), "terminal_outbox_not_found") {
		t.Fatalf("status=%d body=%q", missingRec.Code, missingRec.Body.String())
	}
}

func TestTerminalOutboxAPIHidesInternalErrors(t *testing.T) {
	stub := &terminalOutboxControllerStub{redriveErr: errors.New("token=private-secret")}
	server := NewServer(nil, "127.0.0.1:18011", WithTerminalOutboxController(stub))
	req := httptest.NewRequest(http.MethodPost, "/api/terminal-outbox/redrive", strings.NewReader(`{"id":""}`))
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	server.handleTerminalOutboxRedrive(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "private-secret") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
