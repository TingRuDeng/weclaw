package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexUnclaimedMessageRequiresExplicitOwner(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	setCodexControlForMessageTest(t, h, codexMessageControlFixture{opts: opts, owner: codexControlUnclaimed})
	h.startCodexAgentTask(opts)
	waitForCodexOwnerReply(t, opts)
	if ag.runCalls != 0 || ag.handoffCalls != 0 {
		t.Fatalf("未认领会话 run=%d handoff=%d", ag.runCalls, ag.handoffCalls)
	}
	if text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n"); !strings.Contains(text, "未由本窗口控制") || !strings.Contains(text, "/cx owner remote") {
		t.Fatalf("reply=%q", text)
	}
}

func TestCodexDesktopOwnedMessageRequiresRemoteHandoff(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	setCodexControlForMessageTest(t, h, codexMessageControlFixture{opts: opts, owner: codexControlDesktop})
	h.startCodexAgentTask(opts)
	waitForCodexOwnerReply(t, opts)
	if ag.runCalls != 0 || ag.handoffCalls != 0 {
		t.Fatalf("Desktop 控制期间 run=%d handoff=%d", ag.runCalls, ag.handoffCalls)
	}
	if text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n"); !strings.Contains(text, "已归还 Codex Desktop") || !strings.Contains(text, "重新选择会话") {
		t.Fatalf("reply=%q", text)
	}
}

func TestCodexOwnerDesktopReleaseDoesNotAutoAcquireOnMessage(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	runtime := codexSessionCommandRuntime{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "user-1",
		fields: []string{"/cx", "owner", "desktop"}, agentName: "codex", agent: ag,
		bindingKey: route.bindingKey, ownerBindingKey: route.bindingKey,
		workspaceRoot: route.workspaceRoot,
	}
	result := h.handleCodexOwnerCommand(runtime)
	if !strings.Contains(result.Reply, "已归还") {
		t.Fatalf("release reply=%q", result.Reply)
	}
	h.startCodexAgentTask(opts)
	waitForCodexOwnerReply(t, opts)
	if ag.runCalls != 0 || ag.handoffCalls != 1 {
		t.Fatalf("release 后普通消息 run=%d handoff=%d", ag.runCalls, ag.handoffCalls)
	}
	if text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n"); !strings.Contains(text, "已归还 Codex Desktop") {
		t.Fatalf("reply=%q", text)
	}
}

func TestCodexOtherRemoteOwnerCannotExecuteMessage(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	setCodexControlForMessageTest(t, h, codexMessageControlFixture{
		opts: opts, owner: codexControlRemote,
		routeKey: "other-route", conversationID: "other-conversation",
	})
	h.startCodexAgentTask(opts)
	waitForCodexOwnerReply(t, opts)
	if ag.runCalls != 0 {
		t.Fatalf("其他窗口控制期间执行次数=%d", ag.runCalls)
	}
	if text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n"); !strings.Contains(text, "另一个消息窗口") {
		t.Fatalf("reply=%q", text)
	}
}

func TestCodexUnclaimedFeishuMessageReturnsOwnerCard(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	setCodexControlForMessageTest(t, h, codexMessageControlFixture{opts: opts, owner: codexControlUnclaimed})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	opts.platform = platform.PlatformFeishu
	opts.reply = reply
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return len(reply.Choices) == 1 })
	if ag.runCalls != 0 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("run=%d choices=%#v", ag.runCalls, reply.Choices)
	}
}

func TestCodexRemoteOwnedRuntimeFailureDoesNotReturnOwnerCard(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	opts.platform = platform.PlatformFeishu
	opts.reply = reply

	h.startCodexAgentTask(opts)

	waitUntil(t, func() bool { return len(reply.Texts) > 0 })
	intent := h.codexSessions.controlIntent(route.threadID)
	if len(reply.Choices) != 0 || ag.runCalls != 0 || ag.bindCalls != 0 || ag.handoffCalls != 0 {
		t.Fatalf("runtime 异常不应要求重选 owner，choices=%#v run=%d inspect=%d handoff=%d", reply.Choices, ag.runCalls, ag.bindCalls, ag.handoffCalls)
	}
	if intent.Owner != codexControlRemote || intent.RouteBindingKey != route.bindingKey || intent.ConversationID != route.conversationID {
		t.Fatalf("runtime 异常不应修改 remote owner，intent=%#v", intent)
	}
	if text := strings.Join(reply.Texts, "\n"); !strings.Contains(text, "运行通道暂不可用") || !strings.Contains(text, "所有权保持不变") {
		t.Fatalf("reply=%q", text)
	}
}

func TestCodexBroadcastQueuesBehindActiveDesktopTurn(t *testing.T) {
	h, ag, _, route := liveMessageFixture(t, true)
	h.agents["codex"] = ag
	ag.watchDone = make(chan struct{})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	ctx, cancel := context.WithCancel(context.Background())
	h.broadcastToAgents(broadcastAgentsRequest{
		ctx: ctx, platformName: platform.PlatformWeChat,
		userID: "user-1", routeUserID: "user-1", replyWriter: reply,
		names: []string{"codex"}, message: "广播续跑",
	})
	waitUntil(t, func() bool { return strings.Contains(strings.Join(reply.Texts, "\n"), queuedAgentMessage) })
	if ag.runCalls != 0 {
		t.Fatalf("本地任务执行期间广播启动了新 turn，run=%d", ag.runCalls)
	}
	if cleared, denied := h.clearPendingGuide(route.conversationID, "user-1"); !cleared || denied {
		t.Fatalf("清理测试暂存消息失败，cleared=%v denied=%v", cleared, denied)
	}
	if cancelled, denied := h.cancelActiveTask(route.conversationID, "user-1"); !cancelled || denied {
		t.Fatalf("停止测试任务失败，cancelled=%v denied=%v", cancelled, denied)
	}
	cancel()
	waitUntil(t, func() bool {
		_, active := h.activeTask(route.conversationID)
		return !active
	})
}

type codexMessageControlFixture struct {
	opts           codexAgentTaskOptions
	owner          codexControlOwner
	routeKey       string
	conversationID string
}

func setCodexControlForMessageTest(t *testing.T, h *Handler, fixture codexMessageControlFixture) {
	t.Helper()
	current := h.codexSessions.controlIntent(fixture.opts.route.threadID)
	_, err := h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: fixture.opts.route.threadID, Owner: fixture.owner,
		RouteBindingKey: fixture.routeKey, ConversationID: fixture.conversationID,
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatalf("更新测试控制意图失败: %v", err)
	}
}

func waitForCodexOwnerReply(t *testing.T, opts codexAgentTaskOptions) {
	t.Helper()
	waitUntil(t, func() bool { return len(opts.reply.(*platformtest.Replier).Texts) > 0 })
}
