package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type releaseWatchAgent struct {
	*fakeCodexLiveAgent
	watchStarted  chan struct{}
	watchReturned chan error
}

type releaseBlockingFinalReplier struct {
	*platformtest.Replier
	started chan struct{}
	unblock chan struct{}
}

func (r *releaseBlockingFinalReplier) SendText(ctx context.Context, text string) error {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	select {
	case <-r.unblock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *releaseWatchAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(string)) (string, error) {
	close(a.watchStarted)
	text, err := a.fakeCodexLiveAgent.WatchCodexThread(ctx, conversationID, threadID, onProgress)
	a.watchReturned <- err
	return text, err
}

func TestCodexReleaseClearsOnlyCurrentFrontendBindingWithoutInterrupt(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	state := agent.CodexThreadState{
		ThreadID: "thread-shared", Active: true, ActiveTurnID: "turn-active",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})

	routeKey := codexBindingKey("feishu:tenant:dm:chat:user", "codex")
	otherKey := codexBindingKey("local-frontend", "codex")
	for _, key := range []string{routeKey, otherKey} {
		h.ensureCodexSessions().setActiveWorkspace(key, workspace)
		h.ensureCodexSessions().setThread(key, workspace, "thread-shared")
	}
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	h.ensureCodexSessions().SetFilePath(statePath)

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: "feishu:tenant:dm:chat:user",
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})

	if threadID, pending := h.ensureCodexSessions().getThread(routeKey, workspace); threadID != "" || pending {
		t.Fatalf("released route thread=%q pending=%v, want empty binding", threadID, pending)
	}
	if threadID, pending := h.ensureCodexSessions().getThread(otherKey, workspace); threadID != "thread-shared" || pending {
		t.Fatalf("other frontend thread=%q pending=%v, want thread-shared", threadID, pending)
	}
	if ag.interruptCalls != 0 {
		t.Fatalf("interrupt calls=%d, release must not stop the shared turn", ag.interruptCalls)
	}
	if released, _ := ag.threadHandoffSnapshot(); len(released) != 0 {
		t.Fatalf("host release calls=%v, /cx release must not hand off or restart the host", released)
	}
	wantConversationID := buildCodexConversationID("feishu:tenant:dm:chat:user", "codex", workspace)
	if ag.clearCalledWith != wantConversationID {
		t.Fatalf("cleared conversation=%q, want %q", ag.clearCalledWith, wantConversationID)
	}
	if !strings.Contains(reply, "已解除当前窗口") || !strings.Contains(reply, "本地 Codex 任务继续运行") {
		t.Fatalf("release reply=%q", reply)
	}
	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(statePath)
	if threadID, pending := reloaded.getThread(routeKey, workspace); threadID != "" || pending {
		t.Fatalf("reloaded released route thread=%q pending=%v", threadID, pending)
	}
	if threadID, pending := reloaded.getThread(otherKey, workspace); threadID != "thread-shared" || pending {
		t.Fatalf("reloaded other route thread=%q pending=%v", threadID, pending)
	}
}

func TestDetachCodexFrontendTaskDoesNotCancelInProcessTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	key := buildCodexConversationID("feishu:tenant:dm:chat:user", "codex", "/workspace/project")
	observerDetached := make(chan struct{})
	task, taskCtx, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{
		owner: "user", routeUserID: "feishu:tenant:dm:chat:user", agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
		interactionLease: &agentInteractionLease{}, detachCodexObserver: func() { close(observerDetached) },
	})
	if !started {
		t.Fatal("active task was not started")
	}
	task.mu.Lock()
	task.phase = codexTaskRunning
	task.pending = pendingAgentTask{message: "不得续跑", run: func() { t.Error("detached pending task must not run") }}
	task.mu.Unlock()

	reply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, _, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	task.attachProgressSession(progress)

	result := h.detachCodexFrontendTask(key, "feishu:tenant:dm:chat:user", "thread-active")
	if !result.detached || result.terminal || result.progress != progress {
		t.Fatalf("detach result=%#v", result)
	}
	select {
	case <-taskCtx.Done():
		t.Fatal("release canceled the in-process Codex turn")
	default:
	}
	select {
	case <-observerDetached:
	case <-time.After(time.Second):
		t.Fatal("release did not detach the in-process observer")
	}
	if _, ok := h.activeTask(key); ok {
		t.Fatal("released frontend task is still addressable by /stop")
	}
	select {
	case <-task.done:
	case <-time.After(time.Second):
		t.Fatal("released frontend task did not close its route lifecycle")
	}
	if task.shouldSendFinal() {
		t.Fatal("released frontend task still allows final delivery")
	}
	if pending := task.pendingGuide(); pending != "" {
		t.Fatalf("pending guide=%q, want cleared", pending)
	}
}

func TestCodexReleaseSuppressesFutureApprovalAndUserInputWithoutDefaultDecision(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	})
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-active")
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	lease := &agentInteractionLease{}
	observerDetached := make(chan struct{})
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	_, taskCtx, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user", routeUserID: routeUserID, agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
		interactionLease: lease, detachCodexObserver: func() { close(observerDetached) },
	})
	if !started {
		t.Fatal("active task was not started")
	}
	opts := agentInteractionContextOptions{
		actorUserID: "user", routeUserID: routeUserID, agentName: "codex", reply: reply, lease: lease,
	}
	approval := h.approvalHandlerForRoute(opts)
	userInput := h.userInputHandlerForRoute(opts)

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})
	if !strings.Contains(result, "已解除当前窗口") {
		t.Fatalf("release result=%q", result)
	}
	if _, err := approval(context.Background(), agent.ApprovalRequest{
		RequestID: "approval-after-release",
		Options:   []agent.ApprovalOption{{ID: "allow", Name: "允许"}, {ID: "deny", Name: "拒绝"}},
	}); !errors.Is(err, agent.ErrCodexObserverDetached) {
		t.Fatalf("approval error=%v, want observer detached", err)
	}
	if _, err := userInput(context.Background(), agent.UserInputRequest{
		RequestID: "input-after-release",
		Questions: []agent.UserInputQuestion{{
			ID: "question", Prompt: "请选择", Options: []agent.UserInputOption{{Label: "A"}},
		}},
	}); !errors.Is(err, agent.ErrCodexObserverDetached) {
		t.Fatalf("user input error=%v, want observer detached", err)
	}
	if len(reply.Choices) != 0 {
		t.Fatalf("released route received interaction cards: %#v", reply.Choices)
	}
	select {
	case <-observerDetached:
	default:
		t.Fatal("release did not detach the observer")
	}
	select {
	case <-taskCtx.Done():
		t.Fatal("release canceled the shared turn context")
	default:
	}
}

func TestCodexReleaseRollsBackWhenInteractionLeaseIsInFlight(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	})
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-active")
	lease := &agentInteractionLease{}
	if !lease.begin() {
		t.Fatal("failed to begin interaction lease")
	}
	defer lease.end()
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	_, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user", routeUserID: routeUserID, agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
		interactionLease: lease,
	})
	if !started {
		t.Fatal("active task was not started")
	}
	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})
	if !strings.Contains(result, "正在处理审批或问答") {
		t.Fatalf("release result=%q", result)
	}
	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-active" || pending {
		t.Fatalf("binding after rejected release thread=%q pending=%v", threadID, pending)
	}
}

func TestReleasePendingPersistsOnlyAfterInteractionDetachClaim(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	})
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	h.SetCodexSessionFile(statePath)
	store := h.ensureCodexSessions()
	store.setActiveWorkspace(bindingKey, workspace)
	store.setThread(bindingKey, workspace, "thread-active")

	lease := &agentInteractionLease{}
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	_, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user", routeUserID: routeUserID, agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
		interactionLease: lease,
	})
	if !started {
		t.Fatal("active task was not started")
	}

	pendingWrite := make(chan struct{})
	allowWrite := make(chan struct{})
	store.writeState = func(_ string, data []byte) error {
		var state codexSessionState
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		for _, binding := range state.Bindings {
			for _, session := range binding.Workspaces {
				if !session.ReleasePending {
					continue
				}
				select {
				case <-pendingWrite:
				default:
					close(pendingWrite)
				}
				<-allowWrite
			}
		}
		return nil
	}

	releaseResult := make(chan string, 1)
	go func() {
		releaseResult <- h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
			ActorUserID: "user", RouteUserID: routeUserID,
			Trimmed: "/cx release", Platform: platform.PlatformFeishu,
		})
	}()
	select {
	case <-pendingWrite:
	case <-time.After(time.Second):
		t.Fatal("release did not reach pending persistence")
	}

	interactionClaimed := make(chan bool, 1)
	go func() { interactionClaimed <- lease.begin() }()
	select {
	case claimed := <-interactionClaimed:
		if claimed {
			lease.end()
		}
		t.Fatalf("interaction lease resolved before release persistence completed: claimed=%v", claimed)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowWrite)
	select {
	case result := <-releaseResult:
		if !strings.Contains(result, "已解除当前窗口") {
			t.Fatalf("release result=%q", result)
		}
	case <-time.After(time.Second):
		t.Fatal("release did not finish")
	}
	select {
	case claimed := <-interactionClaimed:
		if claimed {
			lease.end()
			t.Fatal("interaction began after release detach claim")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked interaction did not observe detached lease")
	}
}

func TestCodexReleaseRollbackDoesNotExposeProvisionalTombstone(t *testing.T) {
	store := newCodexSessionStore()
	workspace := t.TempDir()
	bindingKey := codexBindingKey("feishu:bot:dm:chat:user", "codex")
	store.setActiveWorkspace(bindingKey, workspace)
	store.setThread(bindingKey, workspace, "thread-active")

	release, err := store.releaseWorkspaceThread(bindingKey, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.releasedFollowerSnapshots(); len(got) != 0 {
		t.Fatalf("provisional release leaked to recovery: %#v", got)
	}
	if err := store.rollbackWorkspaceThreadRelease(release); err != nil {
		t.Fatal(err)
	}
	if threadID, pending := store.getThread(bindingKey, workspace); threadID != "thread-active" || pending {
		t.Fatalf("rolled back binding thread=%q pending=%v", threadID, pending)
	}
}

func TestDetachCodexFrontendTaskClaimsCardBeforeLateTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	key := buildCodexConversationID("feishu:tenant:dm:chat:user", "codex", "/workspace/project")
	task, _, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{
		owner: "user", routeUserID: "feishu:tenant:dm:chat:user", agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("active task was not started")
	}
	task.mu.Lock()
	task.phase = codexTaskRunning
	task.mu.Unlock()
	reply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	task.attachProgressSession(progress)

	result := h.detachCodexFrontendTask(key, "feishu:tenant:dm:chat:user", "thread-active")
	if !result.detached || result.progress != progress {
		t.Fatalf("detach result=%#v", result)
	}
	if finish("竞态中的迟到终态", false) {
		t.Fatal("late terminal claimed card after frontend detach")
	}
	if reply.stream.completedCount() != 0 {
		t.Fatalf("late terminal completed detached card=%#v", reply.stream.completedSnapshot())
	}
	if err := progress.detachWithoutTerminal(context.Background(), "已解除同步"); err != nil {
		t.Fatal(err)
	}
}

func TestCodexReleaseDetachesActiveDeliveryAndFreezesCard(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-active")

	key := buildCodexConversationID(routeUserID, "codex", workspace)
	task, taskCtx, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{
		owner: "user", routeUserID: routeUserID, agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("active task was not started")
	}
	task.mu.Lock()
	task.phase = codexTaskRunning
	task.mu.Unlock()
	reply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", workspace, "执行任务", cfg,
	)
	task.attachProgressSession(progress)

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})

	if !strings.Contains(result, "已解除当前窗口") {
		t.Fatalf("release result=%q", result)
	}
	select {
	case <-taskCtx.Done():
		t.Fatal("release canceled the active Codex turn")
	default:
	}
	if reply.stream.supersededCount() != 1 {
		t.Fatalf("superseded count=%d, want 1", reply.stream.supersededCount())
	}
	_ = finish("迟到的最终回答", false)
	if reply.stream.completedCount() != 0 {
		t.Fatalf("completed=%#v, released card must stay non-terminal", reply.stream.completedSnapshot())
	}
	if ag.interruptCalls != 0 {
		t.Fatalf("interrupt calls=%d", ag.interruptCalls)
	}
}

func TestCodexReleaseCancelsExternalWatcherWithoutInterruptingSharedTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	state := agent.CodexThreadState{
		ThreadID: "thread-external", Active: true, ActiveTurnID: "turn-external",
	}
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	base.watchDone = make(chan struct{})
	base.watchReply = "本地最终回答"
	ag := &releaseWatchAgent{
		fakeCodexLiveAgent: base,
		watchStarted:       make(chan struct{}), watchReturned: make(chan error, 1),
	}
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-external")
	reply := newReanchorTestReplier()
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeStream
	progressCfg.InitialDelaySeconds = 0

	_, active, err := h.startExternalCodexTaskIfActive(externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user", routeUserID: routeUserID,
		agentName: "codex", agent: ag, conversationID: conversationID,
		threadID: "thread-external", workspaceRoot: workspace,
		platform: platform.PlatformFeishu, progressCfg: progressCfg, reply: reply,
	})
	if err != nil || !active {
		t.Fatalf("start external observer active=%v err=%v", active, err)
	}
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("external watcher did not start")
	}

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})
	if !strings.Contains(result, "已解除当前窗口") {
		t.Fatalf("release result=%q", result)
	}
	select {
	case err := <-ag.watchReturned:
		if err != context.Canceled {
			t.Fatalf("external watcher error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("release did not detach the external watcher")
	}
	waitForRolloverCondition(t, func() bool { return reply.stream.supersededCount() == 1 })
	if reply.stream.completedCount() != 0 {
		t.Fatalf("completed=%#v, released observer must suppress final card delivery", reply.stream.completedSnapshot())
	}
	if ag.interruptCalls != 0 {
		t.Fatalf("interrupt calls=%d", ag.interruptCalls)
	}
	current, err := ag.ReadCodexThreadState(context.Background(), conversationID, "thread-external")
	if err != nil || !current.Active || current.ActiveTurnID != "turn-external" {
		t.Fatalf("shared turn state=%#v err=%v, release must not stop it", current, err)
	}
	close(base.watchDone)
}

func TestCodexReleasePreventsLateAgentMappingFromRebindingThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-old"})
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-old")

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})
	if !strings.Contains(result, "已解除当前窗口") {
		t.Fatalf("release result=%q", result)
	}

	// 模拟任务收尾在解绑之后迟到读取到旧 ACP conversation 映射。
	ag.threadID = "thread-old"
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	h.syncCodexThreadFromAgent(routeUserID, "codex", workspace, ag)
	_, _ = h.recordCodexThreadForWorkspace(routeUserID, "codex", ag, conversationID, workspace)
	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "" || pending {
		t.Fatalf("late mapping rebound released thread=%q pending=%v", threadID, pending)
	}

	// 显式重新选择会话会清除解绑墓碑。
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-new")
	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-new" || pending {
		t.Fatalf("explicit selection thread=%q pending=%v", threadID, pending)
	}
}

func TestCodexReleaseWaitsForPendingInteractionWithoutResolvingIt(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", WaitingOnApproval: true,
	})
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-active")
	pending, err := h.registerPendingApprovalForRoute(
		"user", routeUserID, "approval-key",
		[]agent.ApprovalOption{{ID: "allow", Name: "允许"}, {ID: "deny", Name: "拒绝"}},
		"allow", platform.ChoiceInteractionApproval,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer h.clearPendingApproval("user", pending)

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})
	if !strings.Contains(result, "请先处理当前审批或问答") {
		t.Fatalf("release result=%q", result)
	}
	if threadID, _ := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-active" {
		t.Fatalf("pending interaction release changed binding=%q", threadID)
	}
	select {
	case choice := <-pending.choices:
		t.Fatalf("release resolved pending interaction as %q", choice)
	default:
	}
}

func TestCodexReleaseDoesNotClearBindingChangedWhileWaitingForThreadLock(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexLockWaitTimeout = time.Second
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-old"})
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-old")

	unlockOld, err := h.lockCodexThreadControlContext(context.Background(), "thread-old")
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan string, 1)
	go func() {
		resultCh <- h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
			ActorUserID: "user", RouteUserID: routeUserID,
			Trimmed: "/cx release", Platform: platform.PlatformFeishu,
		})
	}()
	waitForRolloverCondition(t, func() bool {
		unlock, acquired := h.tryLockAgentExecution(codexBindingExecutionKey(bindingKey))
		if acquired {
			unlock()
		}
		return !acquired
	})
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-new")
	ag.threadID = "thread-new"
	unlockOld()

	result := <-resultCh
	if !strings.Contains(result, "绑定状态已变化") {
		t.Fatalf("release result=%q", result)
	}
	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); pending || threadID != "thread-new" {
		t.Fatalf("changed binding thread=%q pending=%v, want thread-new false", threadID, pending)
	}
}

func TestCodexReleaseCannotDetachAfterFinalDeliveryIsClaimed(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	routeUserID := "feishu:tenant:dm:chat:user"
	state := agent.CodexThreadState{ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active"}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-active")

	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user", routeUserID: routeUserID, agentName: "codex",
		codexThreadID: "thread-active", codexTurnID: "turn-active", inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("active task was not started")
	}
	task.mu.Lock()
	task.phase = codexTaskRunning
	task.mu.Unlock()
	reply := &releaseBlockingFinalReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		started: make(chan struct{}), unblock: make(chan struct{}),
	}
	lifecycle := agentTaskLifecycle{
		handler: h,
		opts: agentTaskLifecycleOptions{
			replyCtx: context.Background(), reply: reply, task: task,
			executionKey: conversationID, userID: "user", agentName: "codex",
			cancel: func() {},
		},
		finish: func(string, bool) bool { return false },
	}
	finished := make(chan struct{})
	go func() {
		h.finishAgentTaskLifecycle(lifecycle, "最终回答", nil)
		close(finished)
	}()
	select {
	case <-reply.started:
	case <-time.After(time.Second):
		t.Fatal("final delivery did not start")
	}

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user", RouteUserID: routeUserID,
		Trimmed: "/cx release", Platform: platform.PlatformFeishu,
	})
	if !strings.Contains(result, "任务已进入终态") {
		t.Fatalf("release result=%q", result)
	}
	if threadID, _ := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-active" {
		t.Fatalf("terminal delivery claim lost binding=%q", threadID)
	}
	close(reply.unblock)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("final delivery did not finish")
	}
	h.completeAgentTaskLifecycle(lifecycle)
}

func TestTerminalDeliveryClaimRejectsTaskDetachedByPendingGuide(t *testing.T) {
	h := NewHandler(nil, nil)
	key := "codex-detached-pending-guide"
	task, _, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{
		owner: "user", routeUserID: "route", agentName: "codex",
	})
	if !started {
		t.Fatal("active task was not started")
	}
	task.mu.Lock()
	task.phase = codexTaskRunning
	task.detached = true
	task.mu.Unlock()
	if h.claimActiveTaskTerminal(key, task) {
		t.Fatal("detached task claimed final delivery")
	}
}
