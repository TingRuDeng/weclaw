package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"golang.org/x/net/websocket"
)

func TestCreateCompanionRuntimeSupportsCodexApp(t *testing.T) {
	runtime, err := createCompanionRuntime(agent.CompanionEndpoint{
		Agent:   "codex",
		Command: "codex",
		Cwd:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("createCompanionRuntime() error = %v, want nil", err)
	}
	if runtime == nil {
		t.Fatal("createCompanionRuntime() = nil, want codex runtime")
	}
}

func TestHandleCodexAppMessageFiltersThreadAndBuildsReply(t *testing.T) {
	resultCh := make(chan codexAppTurnResult, 1)
	state := &codexAppTurnState{}
	var progress []string

	done := handleCodexAppMessage([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"other","turnId":"turn-1","delta":"bad"}}`), "thread-1", "turn-1", state, func(text string) {
		progress = append(progress, text)
	}, resultCh)
	if done {
		t.Fatal("其他 thread 的事件不应结束 turn")
	}

	done = handleCodexAppMessage([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","delta":"O"}}`), "thread-1", "turn-1", state, func(text string) {
		progress = append(progress, text)
	}, resultCh)
	if done {
		t.Fatal("delta 事件不应结束 turn")
	}
	done = handleCodexAppMessage([]byte(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"agentMessage","text":"OK"}}}`), "thread-1", "turn-1", state, nil, resultCh)
	if done {
		t.Fatal("agentMessage completed 事件不应结束 turn")
	}
	done = handleCodexAppMessage([]byte(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`), "thread-1", "turn-1", state, nil, resultCh)
	if !done {
		t.Fatal("turn/completed 应结束 turn")
	}

	result := <-resultCh
	if result.err != nil || result.text != "OK" {
		t.Fatalf("result = %#v, want OK", result)
	}
	if strings.Join(progress, "") != "O" {
		t.Fatalf("progress = %#v, want O", progress)
	}
}

func TestHandleCodexAppMessageReturnsTargetError(t *testing.T) {
	resultCh := make(chan codexAppTurnResult, 1)
	state := &codexAppTurnState{}

	done := handleCodexAppMessage([]byte(`{"method":"error","params":{"threadId":"thread-1","turnId":"turn-1","error":{"message":"额度不足"}}}`), "thread-1", "turn-1", state, nil, resultCh)
	if !done {
		t.Fatal("target error 应结束 turn")
	}
	result := <-resultCh
	if result.err == nil || !strings.Contains(result.err.Error(), "额度不足") {
		t.Fatalf("result = %#v, want target error", result)
	}
}

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

func TestCodexAppCompanionRuntimeStartsServerAttachAndRunsTurn(t *testing.T) {
	endpoint := agent.CompanionEndpoint{
		Agent:   "codex",
		Command: "codex",
		Args:    []string{"-c", "model=\"gpt-test\""},
		Cwd:     "/tmp/work",
	}
	fake := &fakeCodexAppSessionClient{reply: "OK"}
	runtime := newCodexAppCompanionRuntime(endpoint)
	runtime.reservePortFn = func() (int, error) { return 45679, nil }
	runtime.newClientFn = func(url string) codexAppSessionClient {
		if url != "ws://127.0.0.1:45679" {
			t.Fatalf("client url=%q, want ws://127.0.0.1:45679", url)
		}
		return fake
	}
	runtime.startServerFn = func(_ context.Context, port int) error {
		if port != 45679 {
			t.Fatalf("server port=%d, want 45679", port)
		}
		return nil
	}
	runtime.waitReadyFn = func(context.Context) error { return nil }
	var attachURL string
	runtime.startAttachFn = func(_ context.Context, url string) error {
		attachURL = url
		return nil
	}

	reply, err := runtime.HandleCompanionRequest(context.Background(), agent.CompanionRequest{
		ConversationID: "wx-1",
		Text:           "只回复 OK",
	}, nil)
	if err != nil {
		t.Fatalf("HandleCompanionRequest() error = %v", err)
	}
	if reply != "OK" {
		t.Fatalf("reply=%q, want OK", reply)
	}
	if attachURL != "ws://127.0.0.1:45679" {
		t.Fatalf("attachURL=%q, want websocket url", attachURL)
	}
	if !reflect.DeepEqual(fake.calls, []string{"connect", "initialize", "startThread:/tmp/work", "startTurn:thread-1:只回复 OK", "waitTurn:thread-1:turn-1"}) {
		t.Fatalf("calls=%#v", fake.calls)
	}
}

func TestCodexAppCommandArgsPreserveConfigAndRemote(t *testing.T) {
	endpoint := agent.CompanionEndpoint{
		Args: []string{"-c", "model=\"gpt-test\""},
		Cwd:  "/tmp/work",
	}
	serverArgs := codexAppServerArgs(endpoint, 45679)
	wantServer := []string{"-c", "model=\"gpt-test\"", "app-server", "--listen", "ws://127.0.0.1:45679"}
	if !reflect.DeepEqual(serverArgs, wantServer) {
		t.Fatalf("serverArgs=%#v, want %#v", serverArgs, wantServer)
	}
	attachArgs := codexAppAttachArgs(endpoint, "ws://127.0.0.1:45679")
	wantAttach := []string{"-c", "model=\"gpt-test\"", "--remote", "ws://127.0.0.1:45679", "--cd", "/tmp/work"}
	if !reflect.DeepEqual(attachArgs, wantAttach) {
		t.Fatalf("attachArgs=%#v, want %#v", attachArgs, wantAttach)
	}
}

func TestCodexAppCommandArgsStripLegacyACPListenArgs(t *testing.T) {
	endpoint := agent.CompanionEndpoint{
		Args: []string{"app-server", "--listen", "stdio://", "-c", "model=\"gpt-test\""},
		Cwd:  "/tmp/work",
	}
	serverArgs := codexAppServerArgs(endpoint, 45679)
	wantServer := []string{"-c", "model=\"gpt-test\"", "app-server", "--listen", "ws://127.0.0.1:45679"}
	if !reflect.DeepEqual(serverArgs, wantServer) {
		t.Fatalf("serverArgs=%#v, want %#v", serverArgs, wantServer)
	}
	attachArgs := codexAppAttachArgs(endpoint, "ws://127.0.0.1:45679")
	wantAttach := []string{"-c", "model=\"gpt-test\"", "--remote", "ws://127.0.0.1:45679", "--cd", "/tmp/work"}
	if !reflect.DeepEqual(attachArgs, wantAttach) {
		t.Fatalf("attachArgs=%#v, want %#v", attachArgs, wantAttach)
	}
}

type fakeCodexAppSessionClient struct {
	calls []string
	reply string
}

func (f *fakeCodexAppSessionClient) Connect(context.Context) error {
	f.calls = append(f.calls, "connect")
	return nil
}

func (f *fakeCodexAppSessionClient) Initialize(context.Context) error {
	f.calls = append(f.calls, "initialize")
	return nil
}

func (f *fakeCodexAppSessionClient) StartThread(_ context.Context, cwd string) (string, error) {
	f.calls = append(f.calls, "startThread:"+cwd)
	return "thread-1", nil
}

func (f *fakeCodexAppSessionClient) StartTurn(_ context.Context, threadID string, text string) (string, error) {
	f.calls = append(f.calls, "startTurn:"+threadID+":"+text)
	return "turn-1", nil
}

func (f *fakeCodexAppSessionClient) WaitTurn(_ context.Context, threadID string, turnID string, _ func(string)) (string, error) {
	f.calls = append(f.calls, "waitTurn:"+threadID+":"+turnID)
	return f.reply, nil
}

func (f *fakeCodexAppSessionClient) Close() error {
	f.calls = append(f.calls, "close")
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
