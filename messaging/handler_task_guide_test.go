package messaging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestRunningCodexStoresSecondMessageAsPendingGuide(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "codex", message: "第一条", clientID: "client-1"})
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), name: "codex", message: "第二条", clientID: "client-2"})
	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("第二条消息不应立即进入 Codex，started=%d", started)
	}
	if !containsText(calls.texts(), queuedAgentMessage) {
		t.Fatalf("未发送引导确认提示，messages=%#v", calls.texts())
	}

	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")
}

func TestStorePendingGuideDoesNotOverwriteExistingMessage(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "shared", activeTaskMeta{owner: "user-1"})
	if !started || !h.storePendingGuide("shared", pendingAgentTask{message: "第二条", run: func() {}}) {
		t.Fatal("首次暂存消息失败")
	}
	if h.storePendingGuide("shared", pendingAgentTask{message: "第三条", run: func() {}}) {
		t.Fatal("已有暂存消息时不应覆盖")
	}
	if got := task.pendingGuide(); got != "第二条" {
		t.Fatalf("pending guide=%q, want first pending message", got)
	}
	h.finishActiveTask("shared", task)
}

type frozenWorkspaceFixture struct {
	h          *Handler
	agent      *blockingCodexThreadAgent
	workspaceA string
	workspaceB string
	bindingKey string
}

// newFrozenWorkspaceFixture 创建任务运行期间尝试使用非 live Agent 切换的测试场景。
func newFrozenWorkspaceFixture(t *testing.T) frozenWorkspaceFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	ag := newBlockingCodexThreadAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setActiveWorkspace(bindingKey, workspaceA)
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	return frozenWorkspaceFixture{
		h: h, agent: ag, workspaceA: workspaceA, workspaceB: workspaceB, bindingKey: bindingKey,
	}
}

// assertFrozenWorkspaceState 验证任务结果仍写回启动时工作空间。
func assertFrozenWorkspaceState(t *testing.T, fixture frozenWorkspaceFixture) {
	t.Helper()
	active, ok := fixture.h.codexSessions.getActiveWorkspace(fixture.bindingKey)
	if !ok || active != normalizeCodexWorkspaceRoot(fixture.workspaceA) {
		t.Fatalf("active workspace=(%q,%v), want %q true", active, ok, normalizeCodexWorkspaceRoot(fixture.workspaceA))
	}
	threadA, pendingA := fixture.h.codexSessions.getThread(fixture.bindingKey, fixture.workspaceA)
	if threadA != "thread-generated-1" || pendingA {
		t.Fatalf("workspace A thread=%q pending=%v, want thread-generated-1 false", threadA, pendingA)
	}
	threadB, pendingB := fixture.h.codexSessions.getThread(fixture.bindingKey, fixture.workspaceB)
	if threadB != "thread-b" || pendingB {
		t.Fatalf("workspace B thread=%q pending=%v, want thread-b false", threadB, pendingB)
	}
}

func TestCodexBackgroundTaskKeepsWorkspaceWhenSwitchAgentUnsupported(t *testing.T) {
	fixture := newFrozenWorkspaceFixture(t)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	handleTestWeChatMessage(fixture.h, ctx, client, newTextMessage(10, "/codex A 任务"))
	waitForCodexThreadAgentEnter(t, fixture.agent)

	conversationA := buildCodexConversationID("user-1", "codex", fixture.workspaceA)
	if got := fixture.agent.conversationCwd(conversationA); got != normalizeCodexWorkspaceRoot(fixture.workspaceA) {
		t.Fatalf("conversation cwd=%q, want %q", got, normalizeCodexWorkspaceRoot(fixture.workspaceA))
	}

	handleTestWeChatMessage(fixture.h, ctx, client, newTextMessage(11, "/cx switch thread-b"))
	handleTestWeChatMessage(fixture.h, ctx, client, newTextMessage(12, "/guide"))

	fixture.agent.release <- struct{}{}
	waitForText(t, calls, "第1条结果")
	assertFrozenWorkspaceState(t, fixture)
	if !containsText(calls.texts(), "当前 Codex Agent 不支持选择即接管") {
		t.Fatalf("非 live Agent 不应改变工作空间，messages=%#v", calls.texts())
	}
	if !containsText(calls.texts(), "当前没有可发送的引导对话") {
		t.Fatalf("/guide should target current B session, messages=%#v", calls.texts())
	}
}

func TestCodexHandlerReturnsWhileTaskRunsSoGuideCanBeStored(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		handleTestWeChatMessage(h, ctx, client, newTextMessage(1, "/codex 第一条"))
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	select {
	case <-firstDone:
	case <-time.After(50 * time.Millisecond):
		ag.release <- struct{}{}
		waitDone(t, firstDone, "第一条任务")
		t.Fatal("Codex Handler 应在任务后台运行后返回，避免串行消息入口阻塞 /guide")
	}

	handleTestWeChatMessage(h, ctx, client, newTextMessage(2, "/codex 第二条"))
	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("第二条消息不应立即进入 Codex，started=%d", started)
	}
	if !containsText(calls.texts(), queuedAgentMessage) {
		t.Fatalf("未发送引导确认提示，messages=%#v", calls.texts())
	}

	ag.release <- struct{}{}
	waitForText(t, calls, "第1条结果")
}

func TestGuideSendsPendingMessageAndSuppressesFirstReply(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "codex", message: "第一条", clientID: "client-1"})
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), name: "codex", message: "第二条", clientID: "client-2"})

	guideDone := make(chan struct{})
	go func() {
		handleTestWeChatMessage(h, ctx, client, newTextMessage(3, "/guide"))
		close(guideDone)
	}()
	waitDone(t, firstDone, "第一条监听")
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitDone(t, guideDone, "引导命令")
	waitForText(t, calls, "第2条结果")

	texts := calls.texts()
	if containsText(texts, "第1条结果") {
		t.Fatalf("第一条任务被引导接管后不应发送最终结果，messages=%#v", texts)
	}
	if !containsText(texts, "第2条结果") {
		t.Fatalf("未发送引导后的最终结果，messages=%#v", texts)
	}
}

func TestCancelWithdrawsPendingGuideAndKeepsRunningTask(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "codex", message: "第一条", clientID: "client-1"})
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), name: "codex", message: "第二条", clientID: "client-2"})

	handleTestWeChatMessage(h, ctx, client, newTextMessage(3, "/cancel"))
	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")
	waitForText(t, calls, "第1条结果")

	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("/cancel 只应撤回暂存消息，不应启动第二条，started=%d", started)
	}
	texts := calls.texts()
	if !containsText(texts, "已撤回该消息。") {
		t.Fatalf("未发送撤回提示，messages=%#v", texts)
	}
	if !containsText(texts, "第1条结果") {
		t.Fatalf("撤回暂存消息后应继续返回第一条结果，messages=%#v", texts)
	}
}

func TestPendingGuideRunsAutomaticallyAfterTaskFinishes(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), name: "codex", message: "第一条", clientID: "client-1"})
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: wechat.NewReplier(client, "user-1", "ctx-1", "client-2"), name: "codex", message: "第二条", clientID: "client-2"})

	ag.release <- struct{}{}
	waitForText(t, calls, "第1条结果")
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitForText(t, calls, "第2条结果")

	started, _ := ag.stats()
	if started != 2 {
		t.Fatalf("上一任务完成后应自动执行暂存消息，started=%d", started)
	}
	if containsText(calls.texts(), "回复“确认”执行该消息") {
		t.Fatalf("自动续跑不应再要求确认，messages=%#v", calls.texts())
	}
}
