package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexSelectionProbeReplier struct {
	*platformtest.Replier
	beforeText func(string)
}

type codexSelectionPlatformCase struct {
	name     string
	platform platform.PlatformName
	actor    string
	route    string
	account  string
	metadata map[string]string
}

func (r *codexSelectionProbeReplier) SendText(ctx context.Context, text string) error {
	if r.beforeText != nil {
		r.beforeText(text)
	}
	return r.Replier.SendText(ctx, text)
}

// TestCodexSelectionAcquiresForWeChatAndFeishu 验证两个真实平台入口都先提交接管，再回复成功。
func TestCodexSelectionAcquiresForWeChatAndFeishu(t *testing.T) {
	cases := []codexSelectionPlatformCase{
		{name: "微信", platform: platform.PlatformWeChat, actor: "wx-actor", route: "wx-actor", account: "wx-bot"},
		{name: "飞书", platform: platform.PlatformFeishu, actor: "fs-actor", route: "feishu:tenant:group:chat:root", account: "fs-bot",
			metadata: map[string]string{feishuSessionMetadataKey: "feishu:tenant:group:chat:root"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { testCodexSelectionAcquiresForPlatform(t, tc) })
	}
}

func testCodexSelectionAcquiresForPlatform(t *testing.T, tc codexSelectionPlatformCase) {
	t.Helper()
	h, ag, workspaceA, workspaceB := newPlatformSelectionFixture(t, tc.route)
	bindingKey := codexBindingKey(tc.route, "codex")
	conversationID := buildCodexConversationID(tc.route, "codex", workspaceB)
	ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(ag.watchDone) })
	probe := &codexSelectionProbeReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
	probe.beforeText = func(text string) {
		assertPlatformSelectionCommitted(t, platformSelectionExpectation{
			h: h, agent: ag,
			bindingKey: bindingKey, conversationID: conversationID,
			actor: tc.actor, route: tc.route, account: tc.account,
			platform: tc.platform, reply: probe,
		})
	}
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: tc.platform, AccountID: tc.account, UserID: tc.actor,
		MessageID: tc.name + "-select", Text: "/cx switch thread-b", Metadata: tc.metadata,
	}, probe)
	if len(probe.Texts) != 1 || !strings.Contains(probe.Texts[0], "已切换并接管") {
		t.Fatalf("texts=%#v，期望单条接管成功回复", probe.Texts)
	}
	if strings.Contains(probe.Texts[0], tc.route) || strings.Contains(probe.Texts[0], tc.account) {
		t.Fatalf("回复泄露路由或账号：%q", probe.Texts[0])
	}
	if active, _ := h.codexSessions.getActiveWorkspace(bindingKey); active != workspaceB {
		t.Fatalf("active=%q，want %q（原工作空间 %q）", active, workspaceB, workspaceA)
	}
	task, active := h.activeTask(conversationID)
	if !active {
		t.Fatal("成功回复后观察任务提前消失")
	}
	closeTestChannel(ag.watchDone)
	waitDone(t, task.done, "平台选择观察任务清理")
}

type platformSelectionExpectation struct {
	h              *Handler
	agent          *fakeCodexLiveAgent
	bindingKey     string
	conversationID string
	actor          string
	route          string
	account        string
	platform       platform.PlatformName
	reply          platform.Replier
}

func newPlatformSelectionFixture(t *testing.T, routeUserID string) (*Handler, *fakeCodexLiveAgent, string, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	root := t.TempDir()
	workspaceA, workspaceB := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("创建测试工作空间失败：%v", err)
		}
	}
	h.SetAllowedWorkspaceRoots([]string{root})
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	h.SetCodexLocalSessionDir(t.TempDir())
	h.defaultName = "codex"
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	h.codexSessions.setActiveWorkspace(bindingKey, workspaceA)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: routeUserID, agentName: "codex", bindingKey: bindingKey,
		workspace: workspaceA, threadID: "thread-a",
	})
	claimDesktopControlForAcquireTest(t, h, "thread-b")
	state := agent.CodexThreadState{ThreadID: "thread-b", Active: true, ActiveTurnID: "turn-b"}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	ag.setThreadBinding("thread-a", desktopAcquireBinding("thread-a"))
	ag.setThreadBinding("thread-b", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeDesktop, State: state})
	h.agents["codex"] = ag
	return h, ag, workspaceA, workspaceB
}

func assertPlatformSelectionCommitted(t *testing.T, want platformSelectionExpectation) {
	t.Helper()
	oldIntent := want.h.codexSessions.controlIntent("thread-a")
	targetIntent := want.h.codexSessions.controlIntent("thread-b")
	if oldIntent.Owner != codexControlDesktop || targetIntent.Owner != codexControlRemote ||
		targetIntent.RouteBindingKey != want.bindingKey || targetIntent.ConversationID != want.conversationID {
		t.Fatalf("回复前控制意图未完整提交：old=%#v target=%#v", oldIntent, targetIntent)
	}
	task, active := want.h.activeTask(want.conversationID)
	if !active || task.owner != want.actor || task.routeUserID != want.route || task.codexThreadID != "thread-b" {
		t.Fatalf("观察任务串线：active=%t task=%#v", active, task)
	}
	task.mu.Lock()
	control := task.externalReservation
	task.mu.Unlock()
	if control == nil {
		t.Fatal("活动目标未创建观察器")
	}
	control.mu.Lock()
	opts := control.runtime.opts
	control.mu.Unlock()
	if opts.platform != want.platform || opts.accountID != want.account || opts.reply != want.reply ||
		opts.actorUserID != want.actor || opts.routeUserID != want.route {
		t.Fatalf("观察上下文串线：opts=%#v", opts)
	}
	requests := want.agent.handoffRequests()
	if len(requests) < 1 || requests[0].Ref.ThreadID != "thread-b" ||
		requests[0].Intent.RouteKey != want.bindingKey || requests[0].Intent.ConversationID != want.conversationID {
		t.Fatalf("runtime request=%#v", requests)
	}
}
