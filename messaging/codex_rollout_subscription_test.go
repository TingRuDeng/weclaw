package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// 模拟独立 App 持有 writer，而 daemon 只能读取会话目录的真实边界。
type readonlyRolloutAgent struct {
	*failingCodexSubscriptionAgent
	reconcileCalls int
}

func (a *readonlyRolloutAgent) ReconcileCodexObservedTurn(context.Context, agent.CodexRuntimeRequest, agent.CodexThreadState) (agent.CodexThreadBinding, error) {
	a.reconcileCalls++
	return agent.CodexThreadBinding{}, errors.New("App turn does not belong to this daemon")
}

func TestAcquireCodexAppRolloutDoesNotSubscribeWriter(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.workspaceB = t.TempDir()
	f.h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceB, "thread-b")
	state := agent.CodexThreadState{ThreadID: "thread-b", ThreadStatus: "notLoaded"}
	f.ag.setBindingState(state)
	f.ag.setThreadBinding("thread-b", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, RuntimeGeneration: 19, State: state})
	ag := &readonlyRolloutAgent{failingCodexSubscriptionAgent: &failingCodexSubscriptionAgent{
		fakeCodexLiveAgent: f.ag, err: errors.New("thread already has an active writer"),
	}}
	dir := t.TempDir()
	writeLocalCodexSession(t, dir, "thread-b", f.workspaceB, "App task", "2026-10-25T08:00:00Z")
	path := localRolloutPathForTest(dir, "thread-b")
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("app-turn"))
	f.h.SetCodexLocalSessionDir(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := f.request("thread-b")
	req.ctx, req.taskContext, req.agent = ctx, ctx, ag
	req.platform, req.accountID = platform.PlatformFeishu, "cli_a"
	req.reply = &codexFollowerRouteReplier{Replier: f.reply, route: platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
	}}
	result, err := f.h.acquireCodexSessionWithBindingLocked(req)
	if err != nil || result.runtimeErr != nil || result.syncErr != nil || !result.externalActive {
		t.Fatalf("App observation must become ready: result=%#v err=%v", result, err)
	}
	if ag.calls != 0 || ag.reconcileCalls != 0 || result.externalState.Controllable {
		t.Fatalf("read-only observation acquired daemon control: subscribe=%d reconcile=%d", ag.calls, ag.reconcileCalls)
	}
	assertReadonlyRolloutDelivery(t, f.h, req.route.conversationID, f.bindingKey, path, "app-turn", f.reply.TextsSnapshot)
}

func TestCodexFollowerRestoresUnloadedAppRollout(t *testing.T) {
	h, base, _, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", ThreadStatus: "notLoaded",
	})
	defer close(watchDone)
	base.binding.RuntimeGeneration = 19
	ag := &readonlyRolloutAgent{failingCodexSubscriptionAgent: &failingCodexSubscriptionAgent{
		fakeCodexLiveAgent: base.fakeCodexLiveAgent, err: errors.New("thread already has an active writer"),
	}}
	h.SetDefaultAgent("codex", ag)
	dir := t.TempDir()
	writeLocalCodexSession(t, dir, "thread-local", snapshot.Target.WorkspaceRoot, "App task", "2026-10-25T08:00:00Z")
	path := localRolloutPathForTest(dir, "thread-local")
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("app-turn"))
	h.SetCodexLocalSessionDir(dir)
	reply := newCodexFollowerDurableReplier(snapshot.Target.DeliveryRoute, platform.Capabilities{Text: true})
	registry := newCodexFollowerTestRegistry(snapshot.Target.DeliveryRoute, reply)
	h.SetPlatformRegistry(registry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.reconcileCodexFollower(ctx, registry, snapshot); err != nil {
		t.Fatalf("restore App observer: %v", err)
	}
	if ag.calls != 0 || ag.reconcileCalls != 0 {
		t.Fatalf("read-only recovery acquired daemon control: subscribe=%d reconcile=%d", ag.calls, ag.reconcileCalls)
	}
	assertReadonlyRolloutDelivery(t, h, snapshot.ConversationID, snapshot.BindingKey, path, "app-turn", reply.TextsSnapshot)
}

func assertReadonlyRolloutDelivery(t *testing.T, h *Handler, conversationID, bindingKey, path, turnID string, texts func() []string) {
	t.Helper()
	snapshot, ok := h.ensureCodexSessions().followerSnapshot(bindingKey)
	if !ok || snapshot.AttachPhase != codexFollowerAttachReady || snapshot.AttachTurnID != turnID || snapshot.RuntimeGeneration != 0 {
		t.Fatalf("read-only observer not ready: %#v", snapshot)
	}
	task, ok := h.activeTask(conversationID)
	if !ok {
		t.Fatal("App task has no progress observer")
	}
	appendCodexRolloutRecord(t, path, rolloutProgressRecord("App 新进度"))
	waitUntil(t, func() bool {
		progress, _ := task.progressCardSnapshot()
		return strings.Contains(progress.text, "App 新进度")
	})
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord(turnID, "App 最终结果"))
	waitUntil(t, func() bool {
		_, active := h.activeTask(conversationID)
		return !active && containsText(texts(), "App 最终结果")
	})
}
