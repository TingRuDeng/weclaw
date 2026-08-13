package messaging

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type contextCheckingCodexLiveAgent struct {
	*fakeCodexLiveAgent
}

type contextCheckingFinalReplier struct {
	*platformtest.Replier
}

func (r *contextCheckingFinalReplier) SendText(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.Replier.SendText(ctx, text)
}

func (a *contextCheckingCodexLiveAgent) ReconcileCodexObservedTurn(
	ctx context.Context, req agent.CodexRuntimeRequest, state agent.CodexThreadState,
) (agent.CodexThreadBinding, error) {
	if err := ctx.Err(); err != nil {
		return agent.CodexThreadBinding{}, err
	}
	return a.fakeCodexLiveAgent.ReconcileCodexObservedTurn(ctx, req, state)
}

func TestExternalCodexTerminalReconcileOutlivesSwitchCallbackContext(t *testing.T) {
	h := NewHandler(nil, nil)
	callbackCtx, cancelCallback := context.WithCancel(context.Background())
	cancelCallback()
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	ag := &contextCheckingCodexLiveAgent{fakeCodexLiveAgent: base}
	runtime := externalCodexTaskRuntime{
		opts: externalCodexTaskOptions{
			ctx: callbackCtx, actorUserID: "user-1", routeUserID: "user-1", agentName: "codex",
			agent: ag, conversationID: "conversation-1", threadID: "thread-1",
		},
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
		ctx: context.Background(),
	}

	err := h.reconcileExternalCodexTerminal(runtime, codexExternalWatchResult{
		Terminal: true, ConfirmedTerminal: true, Final: "任务完成",
	})
	if err != nil {
		t.Fatalf("reconcile error=%v, terminal task context must outlive switch callback", err)
	}
	binding := ag.threadBinding("thread-1")
	if binding.State.Active || binding.State.LastTurnID != "turn-1" || binding.State.LastTurnStatus != "completed" {
		t.Fatalf("binding=%#v, want completed turn", binding)
	}
}

func TestExternalCodexFinalReplyOutlivesSwitchCallbackContext(t *testing.T) {
	h := NewHandler(nil, nil)
	callbackCtx, cancelCallback := context.WithCancel(context.Background())
	cancelCallback()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	reply := &contextCheckingFinalReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
	task, taskCtx := newActiveAgentTask(context.Background(), activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	h.tasks.mu.Lock()
	h.ensureActiveTasksLocked()
	h.tasks.active["conversation-1"] = task
	h.tasks.mu.Unlock()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	runtime := externalCodexTaskRuntime{
		opts: externalCodexTaskOptions{
			ctx: callbackCtx, actorUserID: "user-1", routeUserID: "user-1", agentName: "codex",
			agent: ag, conversationID: "conversation-1", threadID: "thread-1", progressCfg: cfg, reply: reply,
		},
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
		watch: func(context.Context, func(agent.ProgressEvent)) (string, error) {
			return "任务完成", nil
		},
		task: task, ctx: taskCtx,
	}

	h.runExternalCodexTaskWatcher(runtime)
	if !containsText(reply.TextsSnapshot(), "任务完成") {
		t.Fatalf("texts=%#v, final reply must use task lifecycle context", reply.TextsSnapshot())
	}
}

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
		watch: func(context.Context, func(agent.ProgressEvent)) (string, error) {
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

func TestRequestedStopExternalWatcherRendersStoppedTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task, taskCtx, started := h.beginActiveTask(ctx, "conversation-1", activeTaskMeta{
		owner: "user-1", codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	if !started {
		t.Fatal("failed to register active task")
	}
	task.mu.Lock()
	task.phase = codexTaskStopping
	task.mu.Unlock()
	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, Streaming: true, StreamCompletionNotification: true,
	})
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeStream
	progressCfg.SendAcceptance = boolPtr(false)
	runtime := externalCodexTaskRuntime{
		opts: externalCodexTaskOptions{
			ctx: ctx, actorUserID: "user-1", agentName: "codex",
			conversationID: "conversation-1", threadID: "thread-1",
			progressCfg: progressCfg, reply: reply,
		},
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}},
		task: task,
		ctx:  taskCtx,
		watch: func(context.Context, func(agent.ProgressEvent)) (string, error) {
			return "", fmt.Errorf("%w: interrupted", errCodexRolloutAborted)
		},
	}

	h.runExternalCodexTaskWatcher(runtime)

	if reply.Stream.Failed != "" {
		t.Fatalf("stopped task rendered as failure: %q", reply.Stream.Failed)
	}
	if reply.Stream.Completed != "任务已按请求停止。" {
		t.Fatalf("completed=%q, want stopped terminal content", reply.Stream.Completed)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "任务已停止，请查看上方卡片。" {
		t.Fatalf("texts=%#v, want explicit stopped notification", reply.Texts)
	}
	if _, active := h.activeTask("conversation-1"); active {
		t.Fatal("stopped external task was not released")
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
	for _, runtimeHolder := range []agent.CodexRuntimeHolder{agent.CodexRuntimeWeClaw, agent.CodexRuntimeDesktop} {
		t.Run(string(runtimeHolder), func(t *testing.T) {
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
			ag.setBindingRuntime(runtimeHolder)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("%s 重连后 watcher 未恢复", runtimeHolder)
			}
			if _, active := h.activeTask(runtime.opts.conversationID); active {
				t.Fatal("重连后的真实终态未结束任务")
			}
		})
	}
}

func TestCurrentCodexSharedHostBindingRequiresSameTurn(t *testing.T) {
	for _, test := range []struct {
		name    string
		runtime agent.CodexRuntimeHolder
		state   agent.CodexThreadState
		want    bool
	}{
		{name: "desktop active same turn", runtime: agent.CodexRuntimeDesktop, state: agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"}, want: true},
		{name: "desktop inactive same terminal", runtime: agent.CodexRuntimeDesktop, state: agent.CodexThreadState{ThreadID: "thread-1", LastTurnID: "turn-1", LastTurnStatus: "completed"}, want: true},
		{name: "desktop different turn", runtime: agent.CodexRuntimeDesktop, state: agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-2"}},
		{name: "weclaw different turn", runtime: agent.CodexRuntimeWeClaw, state: agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, runtime, cancel := disconnectedExternalRuntimeFixture(t)
			defer cancel()
			ag := runtime.opts.agent.(*fakeCodexLiveAgent)
			ag.setThreadBinding("thread-1", agent.CodexThreadBinding{Runtime: test.runtime, State: test.state})
			_, got, err := h.currentCodexSharedHostBinding(context.Background(), externalCodexWatchRequest{
				agent: ag, routeUserID: "user-1", agentName: "codex", conversationID: "conversation-1",
				threadID: "thread-1", turnID: "turn-1",
			})
			if err != nil || got != test.want {
				t.Fatalf("reconnected=%v err=%v, want %v", got, err, test.want)
			}
		})
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
		watch: func(context.Context, func(agent.ProgressEvent)) (string, error) {
			return "", agent.ErrCodexDesktopDisconnected
		},
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
