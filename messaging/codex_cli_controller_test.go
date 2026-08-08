package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeCodexCLIHostAgent struct {
	*fakeAgent
	host   agent.CodexCLIHost
	err    error
	called bool
}

func (f *fakeCodexCLIHostAgent) PrepareCodexCLIHost(context.Context) (agent.CodexCLIHost, error) {
	f.called = true
	return f.host, f.err
}

func TestPrepareCodexCLIHostUsesRunningCodexAgent(t *testing.T) {
	runtimeAgent := &fakeCodexCLIHostAgent{
		fakeAgent: &fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		host:      agent.CodexCLIHost{SocketPath: "/tmp/shared.sock"},
	}
	h := newTestHandler()
	h.agents["codex"] = runtimeAgent

	host, err := h.PrepareCodexCLIHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeAgent.called || host.SocketPath != "/tmp/shared.sock" {
		t.Fatalf("called=%v host=%#v", runtimeAgent.called, host)
	}
}

func TestPrepareCodexCLIHostPreservesAgentFailure(t *testing.T) {
	wantErr := errors.New("desktop owns host")
	runtimeAgent := &fakeCodexCLIHostAgent{
		fakeAgent: &fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		err:       wantErr,
	}
	h := newTestHandler()
	h.agents["codex"] = runtimeAgent

	_, err := h.PrepareCodexCLIHost(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
}
