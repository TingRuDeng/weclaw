package cmd

import (
	"context"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

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

func TestCodexAppCompanionRuntimeStartOpensVisibleAttachWithoutTurn(t *testing.T) {
	endpoint := agent.CompanionEndpoint{
		Agent:   "codex",
		Command: "codex",
		Cwd:     "/tmp/work",
	}
	fake := &fakeCodexAppSessionClient{reply: "OK"}
	runtime := newCodexAppCompanionRuntime(endpoint)
	runtime.reservePortFn = func() (int, error) { return 45679, nil }
	runtime.newClientFn = func(string) codexAppSessionClient {
		return fake
	}
	runtime.startServerFn = func(context.Context, int) error {
		return nil
	}
	runtime.waitReadyFn = func(context.Context) error {
		return nil
	}
	var attachURL string
	runtime.startAttachFn = func(_ context.Context, url string) error {
		attachURL = url
		return nil
	}

	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if attachURL != "ws://127.0.0.1:45679" {
		t.Fatalf("attachURL=%q, want websocket url", attachURL)
	}
	if !reflect.DeepEqual(fake.calls, []string{"connect", "initialize", "startThread:/tmp/work"}) {
		t.Fatalf("calls=%#v, want eager startup without turn", fake.calls)
	}
}

func TestCodexAppCommandArgsPreserveConfigAndRemote(t *testing.T) {
	endpoint := agent.CompanionEndpoint{
		Args: []string{"-c", "model=\"gpt-test\""},
		Cwd:  "/tmp/work",
	}
	serverArgs := codexAppServerArgs(endpoint, 45679)
	wantServer := []string{"-c", "check_for_update_on_startup=false", "-c", "model=\"gpt-test\"", "app-server", "--listen", "ws://127.0.0.1:45679"}
	if !reflect.DeepEqual(serverArgs, wantServer) {
		t.Fatalf("serverArgs=%#v, want %#v", serverArgs, wantServer)
	}
	attachArgs := codexAppAttachArgs(endpoint, "ws://127.0.0.1:45679")
	wantAttach := []string{"-c", "check_for_update_on_startup=false", "-c", "model=\"gpt-test\"", "--remote", "ws://127.0.0.1:45679", "--cd", "/tmp/work"}
	if !reflect.DeepEqual(attachArgs, wantAttach) {
		t.Fatalf("attachArgs=%#v, want %#v", attachArgs, wantAttach)
	}
}

func TestCodexAppCommandArgsDoNotDuplicateConfiguredUpdateCheck(t *testing.T) {
	endpoint := agent.CompanionEndpoint{
		Args: []string{"-c", "check_for_update_on_startup=true", "-c", "model=\"gpt-test\""},
		Cwd:  "/tmp/work",
	}
	serverArgs := codexAppServerArgs(endpoint, 45679)
	wantServer := []string{"-c", "check_for_update_on_startup=true", "-c", "model=\"gpt-test\"", "app-server", "--listen", "ws://127.0.0.1:45679"}
	if !reflect.DeepEqual(serverArgs, wantServer) {
		t.Fatalf("serverArgs=%#v, want %#v", serverArgs, wantServer)
	}
	attachArgs := codexAppAttachArgs(endpoint, "ws://127.0.0.1:45679")
	wantAttach := []string{"-c", "check_for_update_on_startup=true", "-c", "model=\"gpt-test\"", "--remote", "ws://127.0.0.1:45679", "--cd", "/tmp/work"}
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
	wantServer := []string{"-c", "check_for_update_on_startup=false", "-c", "model=\"gpt-test\"", "app-server", "--listen", "ws://127.0.0.1:45679"}
	if !reflect.DeepEqual(serverArgs, wantServer) {
		t.Fatalf("serverArgs=%#v, want %#v", serverArgs, wantServer)
	}
	attachArgs := codexAppAttachArgs(endpoint, "ws://127.0.0.1:45679")
	wantAttach := []string{"-c", "check_for_update_on_startup=false", "-c", "model=\"gpt-test\"", "--remote", "ws://127.0.0.1:45679", "--cd", "/tmp/work"}
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
