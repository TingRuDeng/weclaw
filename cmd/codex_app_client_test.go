package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestCodexAppClientStartsThreadAndWaitsTurn(t *testing.T) {
	server := httptest.NewServer(websocket.Server{Handshake: allowCodexAppTestHandshake, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		req := receiveCodexAppTestRequest(t, ws)
		sendCodexAppTestResult(t, ws, req.ID, map[string]any{"userAgent": "test", "codexHome": "/tmp", "platformFamily": "unix", "platformOs": "linux"})

		req = receiveCodexAppTestRequest(t, ws)
		if req.Method != "thread/start" || req.Params["cwd"] != "/tmp/work" {
			t.Fatalf("thread/start request = %#v", req)
		}
		sendCodexAppTestResult(t, ws, req.ID, map[string]any{"thread": map[string]any{"id": "thread-1"}})

		req = receiveCodexAppTestRequest(t, ws)
		input := req.Params["input"].([]any)[0].(map[string]any)
		if req.Method != "turn/start" || req.Params["threadId"] != "thread-1" || input["text"] != "只回复 OK" {
			t.Fatalf("turn/start request = %#v", req)
		}
		sendCodexAppTestResult(t, ws, req.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
		sendCodexAppTestNotification(t, ws, "item/agentMessage/delta", map[string]any{"threadId": "other", "turnId": "turn-1", "delta": "bad"})
		sendCodexAppTestNotification(t, ws, "item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "delta": "O"})
		sendCodexAppTestNotification(t, ws, "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"type": "agentMessage", "text": "OK"}})
		sendCodexAppTestNotification(t, ws, "turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed"}})
	}})
	defer server.Close()

	client := newCodexAppClient("ws" + strings.TrimPrefix(server.URL, "http"))
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	threadID, err := client.StartThread(context.Background(), "/tmp/work")
	if err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	turnID, err := client.StartTurn(context.Background(), threadID, "只回复 OK")
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	var progress []string
	reply, err := client.WaitTurn(context.Background(), threadID, turnID, func(text string) {
		progress = append(progress, text)
	})
	if err != nil {
		t.Fatalf("WaitTurn() error = %v", err)
	}
	if reply != "OK" || strings.Join(progress, "") != "O" {
		t.Fatalf("reply=%q progress=%#v, want OK/O", reply, progress)
	}
}

func TestCodexAppClientWaitTurnReturnsWhenConnectionCloses(t *testing.T) {
	server := httptest.NewServer(websocket.Server{Handshake: allowCodexAppTestHandshake, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		req := receiveCodexAppTestRequest(t, ws)
		sendCodexAppTestResult(t, ws, req.ID, map[string]any{"userAgent": "test"})

		req = receiveCodexAppTestRequest(t, ws)
		sendCodexAppTestResult(t, ws, req.ID, map[string]any{"thread": map[string]any{"id": "thread-1"}})

		req = receiveCodexAppTestRequest(t, ws)
		sendCodexAppTestResult(t, ws, req.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	}})
	defer server.Close()

	client := newCodexAppClient("ws" + strings.TrimPrefix(server.URL, "http"))
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	threadID, err := client.StartThread(context.Background(), "/tmp/work")
	if err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	turnID, err := client.StartTurn(context.Background(), threadID, "hello")
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = client.WaitTurn(ctx, threadID, turnID, nil)
	if err == nil || !strings.Contains(err.Error(), "codex app-server 连接已断开") {
		t.Fatalf("WaitTurn() error = %v, want connection closed error", err)
	}
}

func TestCodexAppClientConnectsWithoutOriginHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "origin header is rejected", http.StatusForbidden)
			return
		}
		websocket.Server{Handshake: allowCodexAppTestHandshake, Handler: func(ws *websocket.Conn) {
			_ = ws.Close()
		}}.ServeHTTP(w, r)
	}))
	defer server.Close()

	client := newCodexAppClient("ws" + strings.TrimPrefix(server.URL, "http"))
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v, want nil", err)
	}
	defer client.Close()
}

func allowCodexAppTestHandshake(_ *websocket.Config, _ *http.Request) error {
	return nil
}

type codexAppTestRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func receiveCodexAppTestRequest(t *testing.T, ws *websocket.Conn) codexAppTestRequest {
	t.Helper()
	var req codexAppTestRequest
	if err := websocket.JSON.Receive(ws, &req); err != nil {
		t.Fatalf("Receive request error: %v", err)
	}
	return req
}

func sendCodexAppTestResult(t *testing.T, ws *websocket.Conn, id string, result any) {
	t.Helper()
	if err := websocket.JSON.Send(ws, map[string]any{"id": id, "result": result}); err != nil {
		t.Fatalf("Send result error: %v", err)
	}
}

func sendCodexAppTestNotification(t *testing.T, ws *websocket.Conn, method string, params any) {
	t.Helper()
	if err := websocket.JSON.Send(ws, map[string]any{"method": method, "params": params}); err != nil {
		t.Fatalf("Send notification error: %v", err)
	}
}
