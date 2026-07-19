package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleClaudeIndependentWriterEntriesFailClosed(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "claude", Type: "acp", Command: "claude-agent-acp", LocalCommand: "claude",
	}}}
	h.SetDefaultAgent("claude", ag)

	for _, command := range []string{"/cc cli", "/cc owner", "/cc owner local", "/cc owner remote"} {
		reply := h.handleClaudeSessionCommand(context.Background(), "user-1", command)
		if !strings.Contains(reply, "已停用") || !strings.Contains(reply, "第二个 writer") {
			t.Fatalf("command=%q reply=%q", command, reply)
		}
	}
}

func TestClaudeDisabledEntryDoesNotChangeBinding(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "claude", Type: "acp", Command: "claude-agent-acp",
	}}}
	h.SetDefaultAgent("claude", ag)
	workspace := t.TempDir()
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 7)
	want := h.ensureClaudeSessions().binding(key)

	_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
	_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc cli")
	if got := h.ensureClaudeSessions().binding(key); got != want {
		t.Fatalf("binding=%+v, want unchanged %+v", got, want)
	}
}
