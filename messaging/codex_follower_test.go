package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexFollowerTestPlatform struct {
	name    platform.PlatformName
	account string
	reply   platform.Replier
}

func (p *codexFollowerTestPlatform) Name() platform.PlatformName { return p.name }
func (p *codexFollowerTestPlatform) AccountID() string           { return p.account }
func (p *codexFollowerTestPlatform) Capabilities() platform.Capabilities {
	return p.reply.Capabilities()
}
func (p *codexFollowerTestPlatform) Run(ctx context.Context, _ platform.DispatchFunc) error {
	<-ctx.Done()
	return nil
}
func (p *codexFollowerTestPlatform) NewReplier(chatID string) platform.Replier { return p.reply }
func (p *codexFollowerTestPlatform) NewReplierForRoute(platform.DeliveryRoute) platform.Replier {
	return p.reply
}

type codexFollowerWatchAgent struct {
	*fakeCodexLiveAgent
	watchStarted chan string
}

func (a *codexFollowerWatchAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(string)) (string, error) {
	a.watchStarted <- threadID
	return a.fakeCodexLiveAgent.WatchCodexThread(ctx, conversationID, threadID, onProgress)
}

func TestCodexFollowerAttachesWhenBoundThreadStartsWithoutInboundMessage(t *testing.T) {
	h, ag, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: false,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)

	time.Sleep(30 * time.Millisecond)
	if _, active := h.activeTask(snapshot.ConversationID); active {
		t.Fatal("idle bound thread unexpectedly created an observer")
	}
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
		LastAgentMessageText: "正在检查当前工作区。",
	})
	select {
	case threadID := <-ag.watchStarted:
		if threadID != "thread-local" {
			t.Fatalf("watched thread=%q", threadID)
		}
	case <-time.After(time.Second):
		t.Fatal("follower did not attach after the local turn started")
	}
	task, active := h.activeTask(snapshot.ConversationID)
	if !active {
		t.Fatal("follower observer is not registered as the active delivery task")
	}
	waitForRolloverCondition(t, func() bool {
		task.mu.Lock()
		defer task.mu.Unlock()
		return task.view.lastProgress == "正在检查当前工作区。"
	})

	result := h.detachCodexFrontendTask(snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID)
	if !result.detached {
		t.Fatal("failed to detach follower test observer")
	}
	close(watchDone)
}

func TestCodexFollowerRearmsForSecondLocalTurn(t *testing.T) {
	h, ag, registry, snapshot, firstDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("first local turn was not observed")
	}

	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: false, LastTurnID: "turn-local-1", LastTurnStatus: "completed",
	})
	close(firstDone)
	waitForRolloverCondition(t, func() bool {
		_, active := h.activeTask(snapshot.ConversationID)
		return !active
	})
	secondDone := make(chan struct{})
	ag.fakeCodexThreadAgent.watchDone = secondDone
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-2",
	})
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("follower did not rearm for the second local turn")
	}
	task, active := h.activeTask(snapshot.ConversationID)
	if !active {
		t.Fatal("second local turn observer is not active")
	}
	task.mu.Lock()
	turnID := task.codexTurnID
	task.mu.Unlock()
	if turnID != "turn-local-2" {
		t.Fatalf("observed turn=%q, want turn-local-2", turnID)
	}
	h.detachCodexFrontendTask(snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID)
	close(secondDone)
}

func TestCodexFollowerReleaseDuringProbeStartsNoObserver(t *testing.T) {
	h, ag, registry, snapshot, watchDone := newCodexFollowerFixture(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	entered := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	ag.fakeCodexThreadAgent.threadStateEntered = entered
	ag.fakeCodexThreadAgent.threadStateRelease = releaseProbe
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startCodexFollowerReconciler(ctx, registry, time.Hour)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("follower did not probe the bound thread")
	}
	if _, err := h.ensureCodexSessions().releaseWorkspaceThread(snapshot.BindingKey, snapshot.Target.WorkspaceRoot); err != nil {
		t.Fatal(err)
	}
	close(releaseProbe)
	time.Sleep(50 * time.Millisecond)
	if _, active := h.activeTask(snapshot.ConversationID); active {
		t.Fatal("stale follower probe attached after release")
	}
	select {
	case <-ag.watchStarted:
		t.Fatal("watcher started after follower revision changed")
	default:
	}
	close(watchDone)
}

func TestCodexFollowerRestoresFromPersistedBindingWithoutInboundMessage(t *testing.T) {
	statePath := t.TempDir() + "/codex-sessions.json"
	first, _, _, _, firstDone := newCodexFollowerFixtureWithStatePath(t, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	}, statePath)
	if len(first.ensureCodexSessions().followerSnapshots()) != 1 {
		t.Fatal("fixture follower was not persisted")
	}
	close(firstDone)

	second := NewHandler(nil, nil)
	second.SetCodexSessionFile(statePath)
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-local", Active: true, ActiveTurnID: "turn-local-1",
	})
	base.watchDone = make(chan struct{})
	ag := &codexFollowerWatchAgent{fakeCodexLiveAgent: base, watchStarted: make(chan string, 2)}
	second.SetDefaultAgent("codex", ag)
	snapshots := second.ensureCodexSessions().followerSnapshots()
	if len(snapshots) != 1 {
		t.Fatalf("reloaded followers=%#v", snapshots)
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	registry := newCodexFollowerTestRegistry(snapshots[0].Target.DeliveryRoute, reply)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.startCodexFollowerReconciler(ctx, registry, 10*time.Millisecond)
	select {
	case <-ag.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("persisted follower did not restore after restart")
	}
	second.detachCodexFrontendTask(snapshots[0].ConversationID, snapshots[0].RouteUserID, snapshots[0].Target.ThreadID)
	close(base.watchDone)
}

func newCodexFollowerFixture(t *testing.T, state agent.CodexThreadState) (*Handler, *codexFollowerWatchAgent, *platform.Registry, codexFollowerSnapshot, chan struct{}) {
	t.Helper()
	return newCodexFollowerFixtureWithStatePath(t, state, "")
}

func newCodexFollowerFixtureWithStatePath(t *testing.T, state agent.CodexThreadState, statePath string) (*Handler, *codexFollowerWatchAgent, *platform.Registry, codexFollowerSnapshot, chan struct{}) {
	t.Helper()
	h := NewHandler(nil, nil)
	if statePath != "" {
		h.SetCodexSessionFile(statePath)
	}
	workspace := t.TempDir()
	routeUserID := "feishu:bot:dm:chat:user"
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}
	bindingKey := codexBindingKey(routeUserID, "codex")
	store := h.ensureCodexSessions()
	store.setActiveWorkspace(bindingKey, workspace)
	store.setThread(bindingKey, workspace, "thread-local")
	store.mu.Lock()
	binding := store.bindings[bindingKey]
	binding.FollowRevision++
	binding.Follower = &codexFrontendFollower{
		WorkspaceRoot: workspace, ThreadID: "thread-local", ActorUserID: "user",
		DeliveryRoute: route, UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	store.bindings[bindingKey] = binding
	store.mu.Unlock()
	store.save()
	snapshot := store.followerSnapshots()[0]

	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	watchDone := make(chan struct{})
	base.watchDone = watchDone
	ag := &codexFollowerWatchAgent{fakeCodexLiveAgent: base, watchStarted: make(chan string, 4)}
	h.SetDefaultAgent("codex", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	registry := newCodexFollowerTestRegistry(route, reply)
	return h, ag, registry, snapshot, watchDone
}

func newCodexFollowerTestRegistry(route platform.DeliveryRoute, reply platform.Replier) *platform.Registry {
	return platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &codexFollowerTestPlatform{name: route.Platform, account: route.AccountID, reply: reply},
		Access:   platform.NewAccessControl([]string{"user"}),
	}})
}
