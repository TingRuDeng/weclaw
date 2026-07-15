package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexSwitchDesktopActiveOnlySelectsThread(t *testing.T) {
	state := agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		Preview: "正在审查", Model: "gpt-live", Effort: "high",
	}
	h, ag, workspace := codexLiveSwitchFixture(t, state)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1", platform: platform.PlatformFeishu, reply: reply},
	})
	if ag.useThreadID != "" || ag.handoffCalls != 0 || !strings.Contains(text, "任务正在进行") {
		t.Fatalf("use=%q handoff=%d text=%q", ag.useThreadID, ag.handoffCalls, text)
	}
}

func TestCodexSwitchDesktopIdleShowsModelWithoutHandoff(t *testing.T) {
	state := agent.CodexThreadState{ThreadID: "thread-1", Model: "gpt-live", Effort: "high"}
	h, ag, workspace := codexLiveSwitchFixture(t, state)
	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})
	if ag.handoffCalls != 0 || !strings.Contains(text, "模型: gpt-live") || !strings.Contains(text, "推理强度: high") {
		t.Fatalf("handoff=%d text=%q", ag.handoffCalls, text)
	}
}

func TestCodexSwitchUnclaimedDoesNotTakeControl(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	setSwitchControlIntent(t, h, codexControlUnclaimed)
	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})
	if ag.handoffCalls != 0 || !strings.Contains(text, "控制方: 未认领") {
		t.Fatalf("handoff=%d text=%q", ag.handoffCalls, text)
	}
}

func TestCodexSwitchDoesNotMirrorOtherRemoteRouteTask(t *testing.T) {
	state := agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"}
	h, ag, workspace := codexLiveSwitchFixture(t, state)
	current := h.codexSessions.controlIntent("thread-1")
	_, err := h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlRemote,
		RouteBindingKey: "other-route", ConversationID: "other-conversation",
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}

	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})
	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	if _, active := h.activeTask(conversationID); active {
		t.Fatal("其他远程窗口的任务不应镜像到当前窗口")
	}
	if !strings.Contains(text, "控制方: 其他远程窗口") {
		t.Fatalf("text=%q", text)
	}
}

func TestCodexSwitchProbeFailureKeepsSelection(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	ag.bindErr = errors.New("探测失败")
	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})
	threadID, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if threadID != "thread-1" || pending || !strings.Contains(text, "运行位置探测失败") {
		t.Fatalf("thread=%q pending=%v text=%q", threadID, pending, text)
	}
}

func TestCodexSwitchProbeTimeoutKeepsSelection(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	ag.inspectRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: ctx, userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})
	threadID, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if threadID != "thread-1" || pending || !strings.Contains(text, "运行位置探测超时") ||
		!strings.Contains(text, "会话选择已保留") {
		t.Fatalf("thread=%q pending=%v text=%q", threadID, pending, text)
	}
}

func TestCodexSwitchThreadLockTimeoutKeepsSelection(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	h.codexLockWaitTimeout = 20 * time.Millisecond
	unlockHolder := h.lockCodexThreadControl("thread-1")
	resultCh := make(chan string, 1)
	go func() {
		resultCh <- h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
			ctx: context.Background(), userID: "user-1", agentName: "codex",
			workspaceRoot: workspace, agent: ag, target: "thread-1",
			options: codexSwitchOptions{actorUserID: "user-1"},
		})
	}()

	select {
	case text := <-resultCh:
		unlockHolder()
		if !strings.Contains(text, "运行位置探测超时") || !strings.Contains(text, "会话选择已保留") {
			t.Fatalf("text=%q", text)
		}
	case <-time.After(500 * time.Millisecond):
		unlockHolder()
		t.Fatal("thread 控制锁等待未按时结束")
	}
}

func TestCodexSwitchBlocksDifferentThreadWhileTaskRuns(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-new"})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-old")
	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("未能创建测试任务")
	}
	defer h.finishActiveTask(conversationID, task)
	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-new",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})
	if !strings.Contains(text, "任务执行期间不能切换") || ag.bindCalls != 0 {
		t.Fatalf("inspect=%d text=%q", ag.bindCalls, text)
	}
}

func TestCodexPrepareConversationRechecksRuntimeEveryTurn(t *testing.T) {
	h, ag, _ := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	route := h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
	if err := h.prepareCodexConversation(context.Background(), route, ag); err != nil {
		t.Fatal(err)
	}
	if err := h.prepareCodexConversation(context.Background(), route, ag); err != nil {
		t.Fatal(err)
	}
	if ag.bindCalls != 2 {
		t.Fatalf("inspect=%d，期望每轮重新探测", ag.bindCalls)
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

func setSwitchControlIntent(t *testing.T, h *Handler, owner codexControlOwner) {
	t.Helper()
	current := h.codexSessions.controlIntent("thread-1")
	_, err := h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: owner, ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
}
