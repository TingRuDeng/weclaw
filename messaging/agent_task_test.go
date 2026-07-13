package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestClaudeAgentTaskOpensCardAndReturnsImmediately(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})

	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
	select {
	case <-reply.StreamOpened:
	default:
		t.Fatal("Claude 后台任务返回前必须创建进度卡")
	}
	waitForAgentEnter(t, ag)
	if h.ActiveTaskCount() != 1 {
		t.Fatalf("ActiveTaskCount()=%d，期望后台任务已登记", h.ActiveTaskCount())
	}
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
}

func TestClaudeAgentTaskRejectsCwdBindingChange(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.sendToNamedAgent(agentMessageRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "user-1", routeUserID: "route-1", reply: reply,
		name: "claude", message: "第一条", clientID: "client-1",
	})
	waitForAgentEnter(t, ag)
	result := h.handleCwdWithAccess("/cwd "+t.TempDir(), []string{"route-1"}, true)
	if !strings.Contains(result, "当前 Claude 任务正在运行") {
		t.Fatalf("cwd result=%q，期望拒绝活动任务期间的绑定修改", result)
	}
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
}

func TestClaudeAgentTaskQueuesOneAndRunsAfterFailure(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	ag.err = errors.New("上一任务失败")
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第二条", clientID: "client-2"})
	if !containsText(reply.Texts, queuedAgentMessage) {
		t.Fatalf("texts=%#v，期望单条排队提示", reply.Texts)
	}
	ag.release <- struct{}{}
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
	started, maxActive := ag.stats()
	if started != 2 || maxActive != 1 {
		t.Fatalf("started=%d maxActive=%d，期望失败后串行续跑", started, maxActive)
	}
}

func TestClaudeAgentTaskSurvivesRequestCancellation(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &claudeContextProbeAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}},
		contexts:  make(chan context.Context, 1), release: make(chan struct{}),
	}
	h.SetDefaultAgent("claude", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	ctx, cancel := context.WithCancel(context.Background())
	h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
	var taskCtx context.Context
	select {
	case taskCtx = <-ag.contexts:
	case <-time.After(taskWaitTimeout):
		t.Fatal("未等到 Claude 后台任务启动")
	}
	cancel()
	if taskCtx.Err() != nil {
		t.Fatalf("task context=%v，消息请求结束不应取消 Claude 后台任务", taskCtx.Err())
	}
	close(ag.release)
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
}

func TestClaudeCancelWithdrawsQueuedMessage(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第二条", clientID: "client-2"})

	result := h.handleCancelPendingGuide(claudeTaskCommandRequest(h, reply))
	if result != "已撤回该消息。" {
		t.Fatalf("result=%q", result)
	}
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
	if started, _ := ag.stats(); started != 1 {
		t.Fatalf("started=%d，撤回后不应执行第二条", started)
	}
}

func TestClaudeStopTargetsCurrentAgentOnly(t *testing.T) {
	h, claude := newClaudeAgentTaskFixture()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
	waitForAgentEnter(t, claude)
	codex := &fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}}
	h.agents["codex"] = codex
	codexKey := h.agentExecutionKeyForRoute("user-1", "route-1", "codex", codex)
	codexTask, codexCtx, started := h.beginActiveTask(context.Background(), codexKey, activeTaskMeta{owner: "user-1", agentName: "codex"})
	if !started {
		t.Fatal("准备 Codex 隔离任务失败")
	}

	result := h.handleStopActiveTask(claudeTaskCommandRequest(h, reply))
	if result != "已停止当前任务。" {
		t.Fatalf("result=%q", result)
	}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: claude})
	select {
	case <-codexCtx.Done():
		t.Fatal("停止 Claude 不应取消 Codex 任务")
	default:
	}
	h.finishActiveTask(codexKey, codexTask)
}

func TestClaudeGuideReturnsUnsupportedAndKeepsQueue(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
	waitForAgentEnter(t, ag)
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: "第二条", clientID: "client-2"})

	h.handleGuideCommand(claudeTaskCommandRequest(h, reply))
	if !containsText(reply.Texts, "Claude 当前不支持 /guide") {
		t.Fatalf("texts=%#v", reply.Texts)
	}
	key := h.agentExecutionKeyForRoute("user-1", "route-1", "claude", ag)
	task, ok := h.activeTask(key)
	pending := ""
	if ok {
		pending = task.pendingGuide()
	}
	if !ok || pending != "第二条" {
		t.Fatalf("task=%v pending=%q，/guide 不应消费排队消息", ok, pending)
	}
	ag.release <- struct{}{}
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
}

func newClaudeAgentTaskFixture() (*Handler, *blockingProgressAgent) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}
	h.SetDefaultAgent("claude", ag)
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	h.SetProgressConfig(cfg)
	return h, ag
}

func claudeTaskCommandRequest(h *Handler, reply platform.Replier) taskCommandRequest {
	return taskCommandRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		actorUserID: "user-1", routeUserID: "route-1", reply: reply,
	}
}

type noActiveTaskExpectation struct {
	handler     *Handler
	routeUserID string
	agent       agent.Agent
}

// waitForNoActiveTask 等待指定路由上的后台任务完成。
func waitForNoActiveTask(t *testing.T, want noActiveTaskExpectation) {
	t.Helper()
	key := want.handler.agentExecutionKeyForRoute("user-1", want.routeUserID, "claude", want.agent)
	waitUntil(t, func() bool {
		_, ok := want.handler.activeTask(key)
		return !ok
	})
}

type claudeContextProbeAgent struct {
	fakeAgent
	contexts chan context.Context
	release  chan struct{}
}

func (a *claudeContextProbeAgent) ChatWithProgress(ctx context.Context, _ string, _ string, _ func(string)) (string, error) {
	a.contexts <- ctx
	select {
	case <-a.release:
		return "完成", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestClaudeSecondQueuedMessageDoesNotAcceptThird(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	for index, message := range []string{"第一条", "第二条", "第三条"} {
		h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1", reply: reply, name: "claude", message: message, clientID: "client"})
		if index == 0 {
			waitForAgentEnter(t, ag)
		}
	}
	if !containsText(reply.Texts, "已有一条暂存消息") {
		t.Fatalf("texts=%#v，第三条应被拒绝", reply.Texts)
	}
	ag.release <- struct{}{}
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
	if strings.Contains(strings.Join(reply.Texts, "\n"), "第3条结果") {
		t.Fatalf("texts=%#v，第三条不应执行", reply.Texts)
	}
}
