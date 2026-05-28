package agent

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestCompanionAgentReturnsClearErrorWhenCompanionDisconnected(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	ag := NewCompanionAgent(CompanionAgentConfig{
		Name:    "opencode",
		Command: "opencode",
		Cwd:     t.TempDir(),
	})
	if err := ag.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(ag.Stop)

	_, err := ag.Chat(context.Background(), "u1", "hello")
	if err == nil {
		t.Fatal("Chat() error = nil, want companion disconnected error")
	}
	if got := err.Error(); !containsAll(got, "opencode", "Companion", "weclaw companion") {
		t.Fatalf("Chat() error = %q, want actionable companion hint", got)
	}
}

func TestCompanionAgentSendsInputAndReturnsFinalReply(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	ag := NewCompanionAgent(CompanionAgentConfig{
		Name:    "opencode",
		Command: "opencode",
		Cwd:     t.TempDir(),
	})
	if err := ag.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(ag.Stop)

	endpoint, err := ReadCompanionEndpoint("opencode", ag.Cwd())
	if err != nil {
		t.Fatalf("ReadCompanionEndpoint() error = %v", err)
	}
	done := make(chan string, 1)
	go fakeCompanionClient(t, endpoint, done)

	reply, err := ag.Chat(context.Background(), "u1", "hello from wechat")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if reply != "final from local companion" {
		t.Fatalf("reply = %q", reply)
	}
	if got := <-done; got != "hello from wechat" {
		t.Fatalf("companion received text = %q", got)
	}
}

func TestCompanionAgentAutoLaunchesVisibleTerminal(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	workspace := t.TempDir()
	var launched CompanionLaunchRequest
	ag := NewCompanionAgent(CompanionAgentConfig{
		Name:       "codex",
		Command:    "codex",
		Cwd:        workspace,
		AutoLaunch: true,
		Launch: func(_ context.Context, req CompanionLaunchRequest) error {
			launched = req
			return nil
		},
	})
	if err := ag.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(ag.Stop)

	if launched.Agent != "codex" || launched.Cwd != workspace {
		t.Fatalf("launch request = %#v, want codex/%s", launched, workspace)
	}
	if launched.Executable == "" {
		t.Fatal("launch executable is empty")
	}
}

func TestCompanionShellCommandQuotesArguments(t *testing.T) {
	got := companionShellCommand(CompanionLaunchRequest{
		Executable: "/tmp/weclaw bin/weclaw",
		Agent:      "codex",
		Cwd:        "/tmp/project's dir",
	})
	want := "'/tmp/weclaw bin/weclaw' companion --agent 'codex' --cwd '/tmp/project'\"'\"'s dir'"
	if got != want {
		t.Fatalf("companionShellCommand()=%q, want %q", got, want)
	}
}

func fakeCompanionClient(t *testing.T, endpoint CompanionEndpoint, done chan<- string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", endpoint.Address(), time.Second)
	if err != nil {
		t.Errorf("dial companion endpoint: %v", err)
		return
	}
	defer conn.Close()

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := encoder.Encode(companionEnvelope{
		Type:  companionMessageHello,
		Token: endpoint.Token,
		PID:   12345,
	}); err != nil {
		t.Errorf("send hello: %v", err)
		return
	}

	var request companionEnvelope
	if err := decoder.Decode(&request); err != nil {
		t.Errorf("decode request: %v", err)
		return
	}
	done <- request.Request.Text
	if err := encoder.Encode(companionEnvelope{
		Type: companionMessageResponse,
		ID:   request.ID,
		Response: &companionResponse{
			OK:   true,
			Text: "final from local companion",
		},
	}); err != nil {
		t.Errorf("send response: %v", err)
	}
}
