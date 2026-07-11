package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexDesktopDisconnectDoesNotFinishTask(t *testing.T) {
	h, runtime, cancel := disconnectedExternalRuntimeFixture(t)
	done := make(chan struct{})
	go func() { h.runExternalCodexTaskWatcher(runtime); close(done) }()
	waitUntil(t, func() bool { return taskPhase(runtime.task) == codexTaskDisconnected })
	cancel()
	<-done
	if _, active := h.activeTask(runtime.opts.conversationID); !active {
		t.Fatal("Desktop 断线误结束了任务")
	}
}

func TestCodexDesktopDisconnectDoesNotRunPendingMessage(t *testing.T) {
	h, runtime, cancel := disconnectedExternalRuntimeFixture(t)
	ran := make(chan struct{}, 1)
	h.storePendingGuide(runtime.opts.conversationID, pendingAgentTask{message: "下一条", run: func() { ran <- struct{}{} }})
	done := make(chan struct{})
	go func() { h.runExternalCodexTaskWatcher(runtime); close(done) }()
	waitUntil(t, func() bool { return taskPhase(runtime.task) == codexTaskDisconnected })
	cancel()
	<-done
	select {
	case <-ran:
		t.Fatal("Desktop 断线误执行了 pending")
	default:
	}
}

func TestCodexPendingWaitsForDesktopRelease(t *testing.T) {
	h, runtime, cancel := disconnectedExternalRuntimeFixture(t)
	h.storePendingGuide(runtime.opts.conversationID, pendingAgentTask{message: "下一条", run: func() {}})
	done := make(chan struct{})
	go func() { h.runExternalCodexTaskWatcher(runtime); close(done) }()
	waitUntil(t, func() bool { return taskPhase(runtime.task) == codexTaskDisconnected })
	if runtime.task.pendingGuide() != "下一条" {
		t.Fatal("等待 Desktop release 时丢失 pending")
	}
	cancel()
	<-done
}

func TestCodexReconnectRestoresControlAfterSnapshot(t *testing.T) {
	h, runtime, cancel := disconnectedExternalRuntimeFixture(t)
	defer cancel()
	codexDir := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-1", t.TempDir(), "会话", "2026-07-11T09:00:00Z")
	path := localRolloutPathForTest(codexDir, "thread-1")
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-1"))
	h.SetCodexLocalSessionDir(codexDir)
	ag := runtime.opts.agent.(*fakeCodexLiveAgent)
	ag.watchResults = append(ag.watchResults, fakeCodexWatchResult{text: "重连后完成"})
	done := make(chan struct{})
	go func() { h.runExternalCodexTaskWatcher(runtime); close(done) }()
	waitUntil(t, func() bool { return taskPhase(runtime.task) == codexTaskDisconnected })
	ag.setBindingOwner(agent.CodexOwnerDesktopLive)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Desktop 重连后 watcher 未恢复")
	}
	if _, active := h.activeTask(runtime.opts.conversationID); active {
		t.Fatal("重连后的真实终态未结束任务")
	}
}

func disconnectedExternalRuntimeFixture(t *testing.T) (*Handler, externalCodexTaskRuntime, context.CancelFunc) {
	t.Helper()
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ag := newFakeCodexLiveAgent(agent.CodexOwnerDesktopDisconnected, agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	_, _ = ag.BindCodexThread(ctx, agent.CodexThreadRef{
		ConversationID: "conversation-1", ThreadID: "thread-1",
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	task, taskCtx, _ := h.beginActiveTask(ctx, "conversation-1", activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexOwnerDesktopLive,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	runtime := externalCodexTaskRuntime{
		opts: externalCodexTaskOptions{ctx: ctx, actorUserID: "user-1", routeUserID: "user-1", agentName: "codex",
			agent: ag, conversationID: "conversation-1", threadID: "thread-1", reply: reply},
		state: agentStateForExternalTest(), task: task, ctx: taskCtx,
		watch: func(context.Context, func(string)) (string, error) { return "", agent.ErrCodexDesktopDisconnected },
	}
	return h, runtime, cancel
}

func agentStateForExternalTest() externalCodexTaskState {
	return externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	}, Controllable: true}
}

func taskPhase(task *activeAgentTask) codexTaskPhase {
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.phase
}
