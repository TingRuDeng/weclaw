package messaging

import (
	"context"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexSessionAcquireFixture struct {
	t          *testing.T
	h          *Handler
	agent      *fakeCodexLiveAgent
	routeUser  string
	bindingKey string
	workspaceA string
	workspaceB string
	reply      platform.Replier
}

type codexAcquireStateSnapshot struct {
	active       string
	threadA      string
	threadB      string
	intentA      codexControlIntent
	intentB      codexControlIntent
	handoffCount int
}

func newCodexSessionAcquireFixture(t *testing.T) *codexSessionAcquireFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	h.codexSessions = newCodexSessionStore()
	routeUser := "route-user"
	bindingKey := codexBindingKey(routeUser, "codex")
	fixture := &codexSessionAcquireFixture{
		t: t, h: h, routeUser: routeUser, bindingKey: bindingKey,
		workspaceA: "/workspace/a", workspaceB: "/workspace/b",
		reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
	h.codexSessions.setThread(bindingKey, fixture.workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, fixture.workspaceB, "thread-b")
	h.codexSessions.setActiveWorkspace(bindingKey, fixture.workspaceA)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: routeUser, agentName: "codex", bindingKey: bindingKey,
		workspace: fixture.workspaceA, threadID: "thread-a",
	})
	claimDesktopControlForAcquireTest(t, h, "thread-b")
	fixture.agent = newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	fixture.agent.setThreadBinding("thread-a", desktopAcquireBinding("thread-a"))
	fixture.agent.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	return fixture
}

func desktopAcquireBinding(threadID string) agent.CodexThreadBinding {
	return agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeDesktop,
		State:   agent.CodexThreadState{ThreadID: threadID},
	}
}

func claimDesktopControlForAcquireTest(t *testing.T, h *Handler, threadID string) {
	t.Helper()
	current := h.ensureCodexSessions().controlIntent(threadID)
	_, err := h.ensureCodexSessions().updateControlIntent(codexControlIntentUpdate{
		ThreadID: threadID, Owner: codexControlDesktop, ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatalf("建立 desktop 控制意图失败: %v", err)
	}
}

func (f *codexSessionAcquireFixture) request(threadID string) codexSessionAcquireRequest {
	workspace := f.workspaceB
	if threadID == "thread-a" {
		workspace = f.workspaceA
	}
	return codexSessionAcquireRequest{
		ctx: context.Background(), actorUserID: f.routeUser, routeUserID: f.routeUser,
		agentName: "codex", agent: f.agent,
		route: codexConversationRoute{
			bindingKey: f.bindingKey, workspaceRoot: workspace,
			conversationID: buildCodexConversationID(f.routeUser, "codex", workspace),
			threadID:       threadID,
		},
		platform: platform.PlatformWeChat, reply: f.reply,
	}
}

func (f *codexSessionAcquireFixture) setActiveTarget(turnID string) {
	f.agent.threadState = agent.CodexThreadState{
		ThreadID: "thread-b", Active: true, ActiveTurnID: turnID,
	}
	f.agent.setThreadBinding("thread-b", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeDesktop,
		State: agent.CodexThreadState{
			ThreadID: "thread-b", Active: true, ActiveTurnID: turnID,
		},
	})
}

func (f *codexSessionAcquireFixture) startExternalObservation(threadID string, workspace string, turnID string) (string, context.Context, *activeAgentTask) {
	f.t.Helper()
	conversationID := buildCodexConversationID(f.routeUser, "codex", workspace)
	task, taskCtx, started := f.h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeDesktop, codexThreadID: threadID, codexTurnID: turnID,
	})
	if !started {
		f.t.Fatal("未能建立外部观察任务")
	}
	task.mu.Lock()
	task.externalReservation = &externalCodexTaskReservationControl{status: externalCodexTaskActivated}
	task.mu.Unlock()
	return conversationID, taskCtx, task
}

func (f *codexSessionAcquireFixture) startInProcessCodexTask(threadID string, workspace string) (string, context.Context, *activeAgentTask) {
	f.t.Helper()
	conversationID := buildCodexConversationID(f.routeUser, "codex", workspace)
	task, taskCtx, started := f.h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: threadID,
		inProcessCodexLifecycle: true,
	})
	if !started {
		f.t.Fatal("未能建立本进程 Codex 任务")
	}
	return conversationID, taskCtx, task
}

func (f *codexSessionAcquireFixture) snapshot() codexAcquireStateSnapshot {
	active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey)
	threadA, _ := f.h.ensureCodexSessions().getThread(f.bindingKey, f.workspaceA)
	threadB, _ := f.h.ensureCodexSessions().getThread(f.bindingKey, f.workspaceB)
	return codexAcquireStateSnapshot{
		active: active, threadA: threadA, threadB: threadB,
		intentA:      f.h.ensureCodexSessions().controlIntent("thread-a"),
		intentB:      f.h.ensureCodexSessions().controlIntent("thread-b"),
		handoffCount: len(f.agent.handoffRequests()),
	}
}

func assertCodexAcquireState(t *testing.T, fixture *codexSessionAcquireFixture, want codexAcquireStateSnapshot) {
	t.Helper()
	got := fixture.snapshot()
	got.intentA.UpdatedAt, got.intentB.UpdatedAt = "", ""
	want.intentA.UpdatedAt, want.intentB.UpdatedAt = "", ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state=%#v, want=%#v", got, want)
	}
}

func assertCodexAcquireOriginalState(t *testing.T, fixture *codexSessionAcquireFixture, handoffCount int) {
	t.Helper()
	want := codexAcquireStateSnapshot{
		active: fixture.workspaceA, threadA: "thread-a", threadB: "thread-b",
		intentA: codexControlIntent{
			Owner: codexControlRemote, RouteBindingKey: fixture.bindingKey,
			ConversationID: buildCodexConversationID(fixture.routeUser, "codex", fixture.workspaceA), Revision: 1,
		},
		intentB: codexControlIntent{Owner: codexControlDesktop, Revision: 1},
	}
	want.handoffCount = handoffCount
	assertCodexAcquireState(t, fixture, want)
}
