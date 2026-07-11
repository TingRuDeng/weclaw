package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexDesktopIdleMessageStartsDesktopTurn(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	if ag.bindCalls == 0 || ag.lastChatMessage() != "继续任务" {
		t.Fatalf("bind=%d message=%q", ag.bindCalls, ag.lastChatMessage())
	}
}

func TestCodexDesktopActiveMessageQueuesOnePending(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, true)
	ag.watchDone = make(chan struct{})
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool {
		task, ok := h.activeTask(route.conversationID)
		return ok && task.pendingGuide() == "继续任务"
	})
	task, _ := h.activeTask(route.conversationID)
	defer task.cancel()
	if ag.chatCallCount() != 0 {
		t.Fatal("active Desktop thread 不应开始新 turn")
	}
}

func TestCodexDesktopDisconnectedRejectsStartGuideAndStop(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.setBindingOwner(agent.CodexOwnerDesktopDisconnected)
	ag.bindErr = agent.ErrCodexDesktopDisconnected
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return len(opts.reply.(*platformtest.Replier).Texts) > 0 })
	text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n")
	if ag.chatCallCount() != 0 || !strings.Contains(text, "连接已断开") {
		t.Fatalf("chat=%d text=%q", ag.chatCallCount(), text)
	}
	if _, active := h.activeTask(route.conversationID); active {
		t.Fatal("断线拒绝后不应留下 active task")
	}
}

func TestCodexDesktopGuideUsesCurrentTurn(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	h.defaultName = "codex"
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexOwnerDesktopLive,
		codexThreadID: "thread-1", codexTurnID: "turn-old",
	})
	h.storePendingGuide(route.conversationID, pendingAgentTask{message: "补充要求", run: func() {}})
	text, handled := h.steerPendingGuideToExternalCodex(context.Background(), route.conversationID, "codex", "user-1")
	if !handled || !strings.Contains(text, "已发送") || ag.steerTurnID != "turn-1" {
		t.Fatalf("handled=%v text=%q turn=%q", handled, text, ag.steerTurnID)
	}
	_ = task
}

func TestCodexDesktopStopWaitsForTerminalAndKeepsPending(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexOwnerDesktopLive,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	h.storePendingGuide(route.conversationID, pendingAgentTask{message: "下一条", run: func() {}})
	text, handled := h.interruptExternalCodexTask(context.Background(), route.conversationID, ag, "user-1")
	if !handled || !strings.Contains(text, "等待任务终态") {
		t.Fatalf("handled=%v text=%q", handled, text)
	}
	if taskPhase(task) != codexTaskStopping || task.pendingGuide() != "下一条" {
		t.Fatalf("phase=%s pending=%q", taskPhase(task), task.pendingGuide())
	}
}

func TestCodexPendingMessageRechecksOwnerBeforeAutorun(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	pending := h.pendingCodexTask(opts)
	ag.setBindingOwner(agent.CodexOwnerDesktopDisconnected)
	ag.bindErr = agent.ErrCodexDesktopDisconnected
	pending.run()
	waitUntil(t, func() bool { return len(opts.reply.(*platformtest.Replier).Texts) > 0 })
	if ag.chatCallCount() != 0 {
		t.Fatal("pending 在 owner 断线后仍开始了新 turn")
	}
}

func TestUnauthorizedUserCannotGuideStopOrReadPendingAction(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	h.defaultName = "codex"
	task, _, _ := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", runtimeOwner: agent.CodexOwnerDesktopLive,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	h.storePendingGuide(route.conversationID, pendingAgentTask{message: "私有指令", run: func() {}})
	guide, handled := h.steerPendingGuideToExternalCodex(context.Background(), route.conversationID, "codex", "user-2")
	stop, stopHandled := h.interruptExternalCodexTask(context.Background(), route.conversationID, ag, "user-2")
	if !handled || !stopHandled || !strings.Contains(guide, "只有任务发起人") || !strings.Contains(stop, "只有任务发起人") {
		t.Fatalf("guide=%q stop=%q", guide, stop)
	}
	if task.pendingGuide() != "私有指令" || ag.interruptTurnID != "" || ag.steerTurnID != "" {
		t.Fatal("未授权控制读取或消费了 pending action")
	}
}

func liveMessageFixture(t *testing.T, active bool) (*Handler, *fakeCodexLiveAgent, codexAgentTaskOptions, codexConversationRoute) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	state := agent.CodexThreadState{ThreadID: "thread-1", Active: active, Model: "gpt-live"}
	if active {
		state.ActiveTurnID = "turn-1"
	}
	ag := newFakeCodexLiveAgent(agent.CodexOwnerDesktopLive, state)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-1")
	route := h.codexConversationRouteForSession("user-1", "user-1", "codex", ag)
	_, _ = ag.BindCodexThread(context.Background(), agent.CodexThreadRef{
		ConversationID: route.conversationID, ThreadID: "thread-1",
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	opts := codexAgentTaskOptions{
		ctx: context.Background(), userID: "user-1", routeUserID: "user-1", reply: reply,
		agentName: "codex", message: "继续任务", agent: ag, progressCfg: cfg, route: route,
	}
	return h, ag, opts, route
}
