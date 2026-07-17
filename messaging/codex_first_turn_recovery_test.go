package messaging

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCommitCodexFirstTurnReplacementUpdatesSelectionAndActiveTask(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := filepath.Join(t.TempDir(), "project")
	bindingKey := codexBindingKey("feishu:window-1", "codex")
	conversationID := buildCodexConversationID("feishu:window-1", "codex", workspace)
	snapshot := h.codexSessions.remoteSelectionSnapshot(bindingKey, "thread-old")
	_, err := h.codexSessions.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace, TargetThreadID: "thread-old",
		ConversationID: conversationID, PendingFirstTurn: true, Expected: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &activeAgentTask{codexThreadID: "thread-old"}
	opts := codexControlledTurnOptions{
		ctx: context.Background(), task: task,
		route: codexConversationRoute{
			bindingKey: bindingKey, workspaceRoot: workspace,
			conversationID: conversationID, threadID: "thread-old",
		},
	}

	err = h.commitCodexFirstTurnReplacement(opts,
		agent.CodexThreadRef{ConversationID: conversationID, ThreadID: "thread-old"},
		agent.CodexThreadRef{ConversationID: conversationID, ThreadID: "thread-new"},
	)
	if err != nil {
		t.Fatal(err)
	}
	threadID, pendingNew := h.codexSessions.getThread(bindingKey, workspace)
	if threadID != "thread-new" || pendingNew || !h.codexSessions.isPendingFirstTurn(bindingKey, workspace, threadID) {
		t.Fatalf("thread=%q pendingNew=%v pendingFirstTurn=%v", threadID, pendingNew, h.codexSessions.isPendingFirstTurn(bindingKey, workspace, threadID))
	}
	task.mu.Lock()
	activeThreadID := task.codexThreadID
	task.mu.Unlock()
	if activeThreadID != "thread-new" {
		t.Fatalf("active task thread=%q", activeThreadID)
	}
}
