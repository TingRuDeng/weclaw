package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleClaudeCliOpensCurrentSession(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeClaudeSessionAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	if err := h.claudeSessions.commitSelection(claudeBindingKey("user-1", "claude"), workspace, "session-current"); err != nil {
		t.Fatal(err)
	}
	var opened []recordedClaudeCLIResume
	h.SetClaudeCLIResumeOpener(func(_ context.Context, command string, workspaceRoot string, sessionID string) error {
		opened = append(opened, recordedClaudeCLIResume{command: command, workspace: workspaceRoot, sessionID: sessionID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(303, "/cc cli"))

	if len(opened) != 1 || opened[0].workspace != workspace || opened[0].sessionID != "session-current" {
		t.Fatalf("opened=%#v, want current session in workspace %s", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Claude CLI") {
		t.Fatalf("reply should mention opened cli, messages=%#v", calls.texts())
	}
}
