package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexSwitchDesktopActiveAcquiresAndObservesTask(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.setActiveTarget("turn-b")
	fixture.agent.watchDone = make(chan struct{})
	defer close(fixture.agent.watchDone)
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	assertCodexSwitchAcquired(t, fixture, text)
	if fixture.agent.useThreadID != "" || !strings.Contains(text, "已开始回传") ||
		!strings.Contains(text, "/guide") || !strings.Contains(text, "/stop") {
		t.Fatalf("use=%q text=%q", fixture.agent.useThreadID, text)
	}
	conversationID := buildCodexConversationID(fixture.routeUser, "codex", fixture.workspaceB)
	if task, active := fixture.h.activeTask(conversationID); !active || task.codexThreadID != "thread-b" || task.codexTurnID != "turn-b" {
		t.Fatalf("active=%t task=%#v", active, task)
	}
}

func TestCodexSwitchDesktopIdleAcquiresWithoutUse(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.setThreadBinding("thread-b", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeDesktop,
		State:   agent.CodexThreadState{ThreadID: "thread-b", Model: "gpt-live", Effort: "high"},
	})
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	assertCodexSwitchAcquired(t, fixture, text)
	if fixture.agent.useThreadID != "" || !strings.Contains(text, "模型: gpt-live") ||
		!strings.Contains(text, "推理强度: high") {
		t.Fatalf("use=%q text=%q", fixture.agent.useThreadID, text)
	}
}

func TestCodexSwitchUnclaimedAcquiresRemoteControl(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	current := fixture.h.codexSessions.controlIntent("thread-b")
	if _, err := fixture.h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-b", Owner: codexControlUnclaimed, ExpectedRevision: current.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	assertCodexSwitchAcquired(t, fixture, text)
}

func TestCodexSwitchRejectsOtherRemoteAndKeepsA(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	current := fixture.h.codexSessions.controlIntent("thread-b")
	if _, err := fixture.h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-b", Owner: codexControlRemote, RouteBindingKey: "other-route",
		ConversationID: "other-conversation", ExpectedRevision: current.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	want := fixture.snapshot()
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	assertCodexAcquireState(t, fixture, want)
	if !strings.Contains(text, "其他远程窗口正在控制") ||
		strings.Contains(text, "other-route") || strings.Contains(text, "other-conversation") {
		t.Fatalf("text=%q", text)
	}
}

func TestCodexSwitchIdempotentSelectionIgnoresProbeFailure(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	setAcquireTargetRemoteForCurrentRoute(t, fixture)
	fixture.agent.inspectErrors["thread-b"] = errors.New("探测失败")
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	if !strings.Contains(text, "已切换并接管") || fixture.agent.bindCalls != 0 {
		t.Fatalf("text=%q", text)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("target owner=%#v", got)
	}
}

func TestCodexSwitchIdempotentSelectionDoesNotWaitForProbe(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	setAcquireTargetRemoteForCurrentRoute(t, fixture)
	fixture.agent.inspectRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(ctx))
	if !strings.Contains(text, "已切换并接管") || fixture.agent.bindCalls != 0 {
		t.Fatalf("text=%q", text)
	}
}

func TestCodexSwitchRuntimeFailureKeepsNewOwnerAndAgent(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	if err := fixture.h.ensureAgentSessions().Set(fixture.routeUser, "claude"); err != nil {
		t.Fatal(err)
	}
	fixture.agent.handoffErrors["thread-b"] = context.DeadlineExceeded
	fixture.agent.inspectErrors["thread-b"] = errors.New("校准失败")
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))

	if selected, ok := fixture.h.ensureAgentSessions().Get(fixture.routeUser); !ok || selected != "codex" {
		t.Fatalf("selected=%q ok=%t, want codex", selected, ok)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote || got.RouteBindingKey != fixture.bindingKey {
		t.Fatalf("target owner=%#v", got)
	}
	if !strings.Contains(text, "已切换并接管") || !strings.Contains(text, "所有权已保留") ||
		strings.Contains(text, "仍保持切换前的 Agent") {
		t.Fatalf("text=%q", text)
	}
}

func TestCodexSwitchThreadLockTimeoutKeepsA(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.h.codexLockWaitTimeout = 20 * time.Millisecond
	unlockHolder := fixture.h.lockCodexThreadControl("thread-b")
	want := fixture.snapshot()
	resultCh := make(chan string, 1)
	go func() {
		resultCh <- fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	}()

	select {
	case text := <-resultCh:
		unlockHolder()
		assertCodexAcquireState(t, fixture, want)
		if !strings.Contains(text, "本次选择未执行") || strings.Contains(text, "已保留") {
			t.Fatalf("text=%q", text)
		}
	case <-time.After(500 * time.Millisecond):
		unlockHolder()
		t.Fatal("thread 控制锁等待未按时结束")
	}
}

func (f *codexSessionAcquireFixture) switchRequest(ctx context.Context) codexSwitchRequest {
	return codexSwitchRequest{
		ctx: ctx, userID: f.routeUser, agentName: "codex", workspaceRoot: f.workspaceB,
		agent: f.agent, target: "thread-b", ownerBindingKey: f.bindingKey,
		options: codexSwitchOptions{
			actorUserID: f.routeUser, platform: platform.PlatformFeishu, reply: f.reply,
		},
	}
}

func assertCodexSwitchAcquired(t *testing.T, fixture *codexSessionAcquireFixture, text string) {
	t.Helper()
	active, _ := fixture.h.codexSessions.getActiveWorkspace(fixture.bindingKey)
	intentA := fixture.h.codexSessions.controlIntent("thread-a")
	intentB := fixture.h.codexSessions.controlIntent("thread-b")
	if active != fixture.workspaceB || intentA.Owner != codexControlDesktop ||
		intentB.Owner != codexControlRemote || intentB.RouteBindingKey != fixture.bindingKey {
		t.Fatalf("active=%q intentA=%#v intentB=%#v", active, intentA, intentB)
	}
	if !strings.HasPrefix(text, "已切换并接管。") || !strings.Contains(text, "控制方: 当前远程窗口") ||
		!strings.Contains(text, "运行位置: Codex Desktop") {
		t.Fatalf("text=%q", text)
	}
}

func setAcquireTargetRemoteForCurrentRoute(t *testing.T, fixture *codexSessionAcquireFixture) {
	t.Helper()
	current := fixture.h.codexSessions.controlIntent("thread-b")
	_, err := fixture.h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-b", Owner: codexControlRemote,
		RouteBindingKey:  fixture.bindingKey,
		ConversationID:   buildCodexConversationID(fixture.routeUser, "codex", fixture.workspaceB),
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCodexSwitchBlocksDifferentThreadWhileTaskRuns(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	conversationID := buildCodexConversationID(fixture.routeUser, "codex", fixture.workspaceA)
	task, _, started := fixture.h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: fixture.routeUser, codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("未能创建测试任务")
	}
	defer fixture.h.finishActiveTask(conversationID, task)
	want := fixture.snapshot()
	text := fixture.h.handleCodexSwitchForRouteWithOptions(fixture.switchRequest(context.Background()))
	assertCodexAcquireState(t, fixture, want)
	if !strings.Contains(text, "当前远程任务仍在执行") || fixture.agent.bindCalls != 0 {
		t.Fatalf("inspect=%d text=%q", fixture.agent.bindCalls, text)
	}
}

func TestCodexPrepareConversationUsesBoundRuntimeWithoutProbe(t *testing.T) {
	h, ag, _ := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	route := h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
	if err := h.prepareCodexConversation(context.Background(), route, ag); err != nil {
		t.Fatal(err)
	}
	if err := h.prepareCodexConversation(context.Background(), route, ag); err != nil {
		t.Fatal(err)
	}
	if ag.bindCalls != 0 || ag.handoffCalls != 0 {
		t.Fatalf("prepare 不应探测或移交 runtime，inspect=%d handoff=%d", ag.bindCalls, ag.handoffCalls)
	}
}

func codexLiveSwitchFixture(t *testing.T, state agent.CodexThreadState) (*Handler, *fakeCodexLiveAgent, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "user-1", agentName: "codex", bindingKey: bindingKey,
		workspace: workspace, threadID: "thread-1",
	})
	return h, ag, workspace
}
