package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestBeginDrainRefusesActiveTaskAndRestoresAdmission(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("beginActiveTask started=false")
	}

	result, err := h.Drain(context.Background(), false)
	if !errors.Is(err, ErrActiveTasksRunning) || result.ActiveTasks != 1 {
		t.Fatalf("Drain result=%#v err=%v, want active-task refusal", result, err)
	}
	second, _, secondStarted := h.beginActiveTask(context.Background(), "task-2", activeTaskMeta{owner: "user-2"})
	if !secondStarted {
		t.Fatal("failed ordinary drain must restore task admission")
	}
	h.finishActiveTask("task-2", second)
	h.finishActiveTask("task-1", task)
}

func TestForceDrainCancelsTasksWaitsAndRejectsNewAdmission(t *testing.T) {
	h := NewHandler(nil, nil)
	task, taskCtx, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("beginActiveTask started=false")
	}
	go func() {
		<-taskCtx.Done()
		h.completeActiveTask("task-1", task)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Drain(ctx, true)
	if err != nil {
		t.Fatalf("Drain force: %v", err)
	}
	if result.ActiveTasks != 1 || result.RemainingTasks != 0 {
		t.Fatalf("Drain result=%#v, want one cancelled and no remaining tasks", result)
	}
	if taskCtx.Err() == nil {
		t.Fatal("force drain did not cancel active task context")
	}
	if next, _, nextStarted := h.beginActiveTask(context.Background(), "task-2", activeTaskMeta{}); nextStarted || next != nil {
		t.Fatal("draining handler accepted a new task")
	}
}

func TestForceDrainCancelsNonLiveCodexBackend(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingCodexThreadAgent()
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-non-live")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.startCodexAgentTask(codexAgentTaskOptions{
		ctx: context.Background(), platform: platform.PlatformFeishu,
		userID: "user", routeUserID: routeUserID,
		reply:     platformtest.NewReplier(platform.Capabilities{Text: true}),
		agentName: "codex", message: "执行任务", agent: ag, progressCfg: cfg,
		route: codexConversationRoute{
			bindingKey: bindingKey, workspaceRoot: workspace,
			conversationID: conversationID, threadID: "thread-non-live",
		},
	})
	waitForCodexThreadAgentEnter(t, ag)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Drain(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemainingTasks != 0 {
		t.Fatalf("non-live Codex backend remained during drain: %#v", result)
	}
	if _, active := h.activeTask(conversationID); active {
		t.Fatal("non-live Codex task remained active after force drain")
	}
}

func TestForceDrainPreservesExternalCodexRecoveryWithoutStoppedTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	outbox, _ := attachTestTerminalOutbox(t, h)
	reply := newReanchorTestReplier()
	reply.route = platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat",
	}
	watchStarted := make(chan struct{})
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}},
		watch: func(ctx context.Context, _ func(agent.ProgressEvent)) (string, error) {
			close(watchStarted)
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user", routeUserID: "route",
		agentName: "codex", conversationID: "conversation", threadID: "thread-1",
		workspaceRoot: "/workspace/project", platform: platform.PlatformFeishu,
		accountID: "bot", reply: reply,
		progressCfg: config.ProgressConfig{
			Mode: progressModeStream, InitialDelaySeconds: 0, SendAcceptance: boolPtr(false),
		},
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil || !h.activateExternalCodexTaskReservation(reservation) {
		t.Fatalf("activate observer err=%v", err)
	}
	select {
	case <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("external observer did not start")
	}
	waitForRolloverCondition(t, func() bool {
		outbox.mu.Lock()
		defer outbox.mu.Unlock()
		return len(outbox.entries) == 1 && outbox.entries[0].Stream != nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Drain(ctx, true)
	if err != nil || result.RemainingTasks != 0 {
		t.Fatalf("Drain result=%#v err=%v", result, err)
	}
	reservation.task.mu.Lock()
	preserved := reservation.task.preserveRecoveryOnDrain
	phase := reservation.task.phase
	reservation.task.mu.Unlock()
	if !preserved || phase == codexTaskTerminal || phase == codexTaskStopping {
		t.Fatalf("observer drain state preserved=%v phase=%s", preserved, phase)
	}
	outbox.mu.Lock()
	entries := make([]*terminalOutboxEntry, len(outbox.entries))
	for index, entry := range outbox.entries {
		entries[index] = cloneTerminalOutboxEntry(entry)
	}
	outbox.mu.Unlock()
	if len(entries) != 1 || entries[0].Stream == nil || entries[0].Checkpoint != nil || entries[0].Text == "" {
		t.Fatalf("drain did not preserve active recovery: %#v", entries)
	}
	if reply.stream.completedCount() != 0 {
		t.Fatalf("drain completed progress card: %#v", reply.stream.completedSnapshot())
	}
	reply.stream.mu.Lock()
	failed := len(reply.stream.failed)
	reply.stream.mu.Unlock()
	if failed != 0 {
		t.Fatalf("drain failed progress card: failed=%d", failed)
	}
}

func TestForceDrainDetachesPendingExternalApprovalWithoutDefaultDeny(t *testing.T) {
	h := NewHandler(nil, nil)
	lease := &agentInteractionLease{}
	task, taskCtx, started := h.beginActiveTask(context.Background(), "conversation", activeTaskMeta{
		owner: "user", routeUserID: "feishu:tenant:dm:chat:user", agentName: "codex",
		codexThreadID: "thread-1", codexTurnID: "turn-1", interactionLease: lease,
	})
	if !started {
		t.Fatal("external Codex task was not started")
	}
	task.mu.Lock()
	task.externalReservation = &externalCodexTaskReservationControl{}
	task.mu.Unlock()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	handler := h.approvalHandlerForRoute(agentInteractionContextOptions{
		actorUserID: "user", routeUserID: "feishu:tenant:dm:chat:user",
		agentName: "codex", reply: reply, lease: lease,
	})
	type approvalResult struct {
		decision string
		err      error
	}
	resultCh := make(chan approvalResult, 1)
	go func() {
		decision, err := handler(taskCtx, agent.ApprovalRequest{
			RequestID: "approval-on-drain",
			Options: []agent.ApprovalOption{
				{ID: "allow", Name: "允许"},
				{ID: "deny", Name: "拒绝"},
			},
		})
		resultCh <- approvalResult{decision: decision, err: err}
		h.finishActiveTask("conversation", task)
	}()
	waitUntil(t, func() bool {
		return h.hasPendingInteractionForRoute("user", "feishu:tenant:dm:chat:user")
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := h.Drain(ctx, true); err != nil {
		t.Fatalf("Drain force: %v", err)
	}
	result := <-resultCh
	if result.decision != "" || !errors.Is(result.err, agent.ErrCodexObserverDetached) {
		t.Fatalf("approval result decision=%q err=%v, want observer detach without decision", result.decision, result.err)
	}
}

func TestForceDrainDetachesInProcessCodexObserverWithoutTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	outbox, _ := attachTestTerminalOutbox(t, h)
	reply := newReanchorTestReplier()
	reply.route = platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat",
	}
	detached := make(chan struct{})
	task, taskCtx, started := h.beginActiveTask(context.Background(), "conversation", activeTaskMeta{
		owner: "user", routeUserID: "route", agentName: "codex",
		codexThreadID: "thread-1", inProcessCodexLifecycle: true,
		detachCodexObserver: func() { close(detached) },
	})
	if !started {
		t.Fatal("in-process Codex task was not started")
	}
	lifecycle := h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
		taskCtx: taskCtx, replyCtx: context.Background(), reply: reply,
		task: task, cancel: task.cancel, executionKey: "conversation",
		userID: "user", agentName: "codex", workspaceRoot: "/workspace/project",
		message: "共享任务", progressConfig: config.ProgressConfig{
			Mode: progressModeStream, InitialDelaySeconds: 0, SendAcceptance: boolPtr(false),
		},
	})
	waitForRolloverCondition(t, func() bool {
		outbox.mu.Lock()
		defer outbox.mu.Unlock()
		return len(outbox.entries) == 1 && outbox.entries[0].Stream != nil
	})

	exitCause := make(chan string, 1)
	go func() {
		select {
		case <-detached:
			exitCause <- "observer-detach"
			h.finishAgentTaskLifecycle(lifecycle, "", agent.ErrCodexObserverDetached)
		case <-taskCtx.Done():
			exitCause <- "context-cancel"
			h.finishAgentTaskLifecycle(lifecycle, "", taskCtx.Err())
		}
		h.completeAgentTaskLifecycle(lifecycle)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.Drain(ctx, true)
	if err != nil || result.RemainingTasks != 0 {
		t.Fatalf("Drain result=%#v err=%v", result, err)
	}
	if cause := <-exitCause; cause != "observer-detach" {
		t.Fatalf("in-process Codex drain cause=%q, want observer-detach", cause)
	}
	if reply.stream.completedCount() != 0 {
		t.Fatalf("drain completed progress card: %#v", reply.stream.completedSnapshot())
	}
	reply.stream.mu.Lock()
	failed := len(reply.stream.failed)
	reply.stream.mu.Unlock()
	if failed != 0 {
		t.Fatalf("drain failed progress card: failed=%d", failed)
	}
	outbox.mu.Lock()
	entries := make([]*terminalOutboxEntry, len(outbox.entries))
	for index, entry := range outbox.entries {
		entries[index] = cloneTerminalOutboxEntry(entry)
	}
	outbox.mu.Unlock()
	if len(entries) != 1 || entries[0].Stream == nil || entries[0].Checkpoint != nil {
		t.Fatalf("drain did not preserve in-process recovery: %#v", entries)
	}
}
