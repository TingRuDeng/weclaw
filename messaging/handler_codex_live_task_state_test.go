package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexTaskStoresOwnerRevisionAndThread(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexOwnerDesktopLive,
		ownerRevision: 7, codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	if !started || task.runtimeOwner != agent.CodexOwnerDesktopLive || task.ownerRevision != 7 {
		t.Fatalf("task = %#v", task)
	}
	if task.codexThreadID != "thread-1" || task.phase != codexTaskRunning {
		t.Fatalf("task = %#v", task)
	}
}

func TestCodexTaskTerminalCanOnlyBeClaimedOnce(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{})
	if !task.claimTerminal() {
		t.Fatal("首次终态认领失败")
	}
	if task.claimTerminal() {
		t.Fatal("终态被重复认领")
	}
}

func TestCodexPendingMessageKeepsFrozenThreadRoute(t *testing.T) {
	h := NewHandler(nil, nil)
	pending := h.pendingCodexTask(codexAgentTaskOptions{
		ctx: context.Background(), message: "继续", route: codexConversationRoute{
			conversationID: "conversation-1", threadID: "thread-1",
		},
	})
	if pending.codexRoute.threadID != "thread-1" || pending.codexRoute.conversationID != "conversation-1" {
		t.Fatalf("pending route = %#v", pending.codexRoute)
	}
}

func TestCodexStopPhaseKeepsPendingMessage(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{owner: "user-1"})
	pending := pendingAgentTask{message: "下一条", run: func() {}}
	if !h.storePendingGuide("task-1", pending) {
		t.Fatal("暂存消息失败")
	}
	if cancelled, denied := h.cancelActiveTask("task-1", "user-1"); !cancelled || denied {
		t.Fatalf("cancelled=%v denied=%v", cancelled, denied)
	}
	if task.phase != codexTaskStopping || task.pending.message != "下一条" {
		t.Fatalf("task = %#v", task)
	}
}
