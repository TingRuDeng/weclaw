package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexDesktopLocalInteractionErrorIsNotTerminal(t *testing.T) {
	result := classifyCodexWatchResult("", errors.New("approval response failed"), "desktop")
	if result.Terminal {
		t.Fatalf("result=%#v", result)
	}
	terminal := classifyCodexWatchResult("", agent.ErrCodexTurnTerminal, "desktop")
	if !terminal.Terminal || !terminal.Failed {
		t.Fatalf("terminal=%#v", terminal)
	}
}

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

func TestCodexStoppedExternalWatcherReleasesTaskAndRunsPending(t *testing.T) {
	h, runtime, cancel := disconnectedExternalRuntimeFixture(t)
	defer cancel()
	ran := make(chan struct{}, 1)
	if !h.storePendingGuide(runtime.opts.conversationID, pendingAgentTask{
		message: "下一条",
		run:     func() { ran <- struct{}{} },
	}) {
		t.Fatal("暂存消息失败")
	}
	done := make(chan struct{})
	go func() { h.runExternalCodexTaskWatcher(runtime); close(done) }()
	waitUntil(t, func() bool { return taskPhase(runtime.task) == codexTaskDisconnected })
	if cancelled, denied := h.cancelActiveTask(runtime.opts.conversationID, "user-1"); !cancelled || denied {
		t.Fatalf("cancelled=%v denied=%v", cancelled, denied)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("停止后 watcher 未退出")
	}
	if _, active := h.activeTask(runtime.opts.conversationID); active {
		t.Fatal("停止后仍保留 active task")
	}
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("停止后未续跑 pending")
	}
	binding := runtime.opts.agent.(*fakeCodexLiveAgent).threadBinding("thread-1")
	if !binding.State.Active || binding.State.ActiveTurnID != "turn-1" {
		t.Fatalf("binding=%#v，断线取消不能伪造已确认终态", binding)
	}
}

func TestCodexRolloutWatchErrorIsTerminal(t *testing.T) {
	result := classifyCodexWatchResult("", errors.New("rollout 文件读取失败"), "rollout")
	if !result.Terminal || result.ConfirmedTerminal || !result.Failed {
		t.Fatalf("result=%#v", result)
	}
	aborted := classifyCodexWatchResult("", errCodexRolloutAborted, "rollout")
	if !aborted.Terminal || !aborted.ConfirmedTerminal || !aborted.Failed {
		t.Fatalf("aborted=%#v", aborted)
	}
}

func TestCodexRolloutWatcherReadFailureReleasesTask(t *testing.T) {
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	task, taskCtx, _ := h.beginActiveTask(ctx, "conversation-1", activeTaskMeta{
		owner: "user-1", codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	runtime := externalCodexTaskRuntime{
		opts: externalCodexTaskOptions{
			ctx: ctx, actorUserID: "user-1", agentName: "codex",
			conversationID: "conversation-1", threadID: "thread-1",
			reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
		},
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}},
		task: task,
		ctx:  taskCtx,
		watch: func(context.Context, func(string)) (string, error) {
			return "", errors.New("rollout 文件读取失败")
		},
	}
	done := make(chan struct{})
	go func() { h.runExternalCodexTaskWatcher(runtime); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("rollout 读取失败后 watcher 未退出")
	}
	if _, active := h.activeTask("conversation-1"); active {
		t.Fatal("rollout 读取失败后仍保留 active task")
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
	ag.setBindingRuntime(agent.CodexRuntimeWeClaw)
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
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeUnknown, agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	task, taskCtx, _ := h.beginActiveTask(ctx, "conversation-1", activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexRuntimeWeClaw,
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
