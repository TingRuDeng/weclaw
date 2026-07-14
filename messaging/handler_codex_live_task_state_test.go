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

func TestCodexDisconnectDoesNotOverrideStopping(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{})
	task.markStopping()
	task.markCodexDisconnected()
	if task.phase != codexTaskStopping {
		t.Fatalf("phase=%q, want stopping", task.phase)
	}
}

func TestCodexReconnectDoesNotOverrideStopping(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{})
	task.markStopping()
	task.markCodexRunning(agent.CodexThreadBinding{Owner: agent.CodexOwnerDesktopLive})
	if task.phase != codexTaskStopping {
		t.Fatalf("phase=%q, want stopping", task.phase)
	}
}

func TestRestorePendingGuideRunsAfterTaskWasRemoved(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexOwnerDesktopLive,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	ran := make(chan struct{}, 1)
	pending := pendingAgentTask{message: "下一条", run: func() { ran <- struct{}{} }}
	if !h.storePendingGuide("task-1", pending) {
		t.Fatal("暂存消息失败")
	}
	_, _, _, _, ok, _ := h.takeExternalCodexGuide("task-1", "user-1")
	if !ok {
		t.Fatal("取出引导消息失败")
	}
	if status := h.queuePendingActiveTask("task-1", pendingAgentTask{message: "第三条", run: func() {}}); status != activeTaskPendingOccupied {
		t.Fatalf("status=%v，发送中的引导消息必须继续占用队列", status)
	}
	if _, ok := h.completeActiveTask("task-1", task); ok {
		t.Fatal("引导发送结果未返回前不应提前提升消息")
	}
	h.finishExternalCodexGuide("task-1", task, false)
	select {
	case <-ran:
	default:
		t.Fatal("任务已移除时，发送失败的暂存消息被静默丢弃")
	}
}
