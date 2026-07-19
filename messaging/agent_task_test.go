package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
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

func TestClaudeBroadcastQueuesWithoutWaitingForRunningTask(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	seedClaudeBinding(t, h, "route-1", "claude", h.claudeWorkspaceRoot("claude"), "session-a", 1)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.sendToNamedAgent(agentMessageRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "user-1", routeUserID: "route-1", reply: reply,
		name: "claude", message: "第一条", clientID: "client-1",
	})
	waitForAgentEnter(t, ag)
	done := make(chan struct{})
	go func() {
		h.broadcastToAgents(broadcastAgentsRequest{
			ctx: context.Background(), platformName: platform.PlatformFeishu,
			userID: "user-1", routeUserID: "route-1", replyWriter: reply,
			names: []string{"claude"}, message: "第二条",
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Claude 广播不应等待当前任务结束")
	}
	if !containsText(reply.Texts, queuedAgentMessage) {
		t.Fatalf("texts=%#v，广播消息应进入统一暂存队列", reply.Texts)
	}
	ag.release <- struct{}{}
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
}

func TestClaudeBroadcastBindingFailureDoesNotRegisterTask(t *testing.T) {
	for _, test := range []struct {
		name    string
		binding claudeSessionBinding
		want    string
	}{
		{name: "unbound", binding: newClaudeBinding("/tmp", "", claudeBindingUnbound), want: "没有有效"},
		{name: "runtime unavailable", binding: newClaudeBinding("/tmp", "session-a", claudeBindingResumeFailed), want: "ClaudeHost 暂不可用"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, ag := newClaudeAgentTaskFixture()
			workspace := h.claudeWorkspaceRoot("claude")
			key := claudeBindingKey("route-1", "claude")
			store := h.ensureClaudeSessions()
			test.binding.WorkspaceRoot = workspace
			store.bindings[key] = test.binding
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			h.broadcastToAgents(broadcastAgentsRequest{
				ctx: context.Background(), platformName: platform.PlatformFeishu,
				userID: "user-1", routeUserID: "route-1", replyWriter: reply,
				names: []string{"claude"}, message: "blocked",
			})
			started, _ := ag.stats()
			if h.ActiveTaskCount() != 0 || started != 0 || !containsText(reply.Texts, test.want) {
				t.Fatalf("active=%d started=%d texts=%#v", h.ActiveTaskCount(), started, reply.Texts)
			}
		})
	}
}

func TestClaudeBroadcastBindingRevisionChangePreventsPrompt(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	workspace := h.claudeWorkspaceRoot("claude")
	seedClaudeBinding(t, h, "route-1", "claude", workspace, "session-a", 1)
	unblock := h.lockAgentExecution(claudeSessionExecutionKey("session-a"))
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	done := make(chan struct{})
	go func() {
		h.broadcastToAgents(broadcastAgentsRequest{
			ctx: context.Background(), platformName: platform.PlatformFeishu,
			userID: "user-1", routeUserID: "route-1", replyWriter: reply,
			names: []string{"claude"}, message: "must not prompt",
		})
		close(done)
	}()
	waitUntil(t, func() bool { return h.ActiveTaskCount() == 1 })
	store := h.ensureClaudeSessions()
	store.mu.Lock()
	binding := store.bindings[claudeBindingKey("route-1", "claude")]
	binding.Revision++
	store.bindings[claudeBindingKey("route-1", "claude")] = binding
	store.mu.Unlock()
	unblock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("广播 revision 复核未返回")
	}
	started, _ := ag.stats()
	if started != 0 || !containsText(reply.Texts, "绑定刚刚发生变化") {
		t.Fatalf("started=%d texts=%#v", started, reply.Texts)
	}
}

func TestClaudeQueuedBroadcastRechecksBindingBeforeAutomaticRun(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	workspace := h.claudeWorkspaceRoot("claude")
	seedClaudeBinding(t, h, "route-1", "claude", workspace, "session-a", 1)
	reply := newSynchronizedTextReplier()
	h.sendToNamedAgent(agentMessageRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "user-1", routeUserID: "route-1", reply: reply,
		name: "claude", message: "first",
	})
	waitForAgentEnter(t, ag)
	h.broadcastToAgents(broadcastAgentsRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "user-1", routeUserID: "route-1", replyWriter: reply,
		names: []string{"claude"}, message: "queued broadcast",
	})
	store := h.ensureClaudeSessions()
	if err := store.updateBinding(claudeBindingKey("route-1", "claude"), func(current claudeSessionBinding) claudeSessionBinding {
		current.SessionID = ""
		current.Status = claudeBindingUnbound
		return current
	}); err != nil {
		t.Fatalf("update binding: %v", err)
	}
	ag.release <- struct{}{}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "route-1", agent: ag})
	if text := reply.waitForText(t); !strings.Contains(text, "没有有效") {
		t.Fatalf("text=%q，暂存广播应在续跑时重新校验 binding", text)
	}
	started, _ := ag.stats()
	if started != 1 {
		t.Fatalf("started=%d texts=%#v，暂存广播不应越过 owner 变化", started, reply.texts())
	}
}

type synchronizedTextReplier struct {
	*platformtest.Replier
	mu     sync.Mutex
	textCh chan string
}

func newSynchronizedTextReplier() *synchronizedTextReplier {
	return &synchronizedTextReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		textCh:  make(chan string, 8),
	}
}

func (r *synchronizedTextReplier) SendText(_ context.Context, text string) error {
	r.mu.Lock()
	r.Texts = append(r.Texts, text)
	r.mu.Unlock()
	r.textCh <- text
	return nil
}

func (r *synchronizedTextReplier) waitForText(t *testing.T) string {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case text := <-r.textCh:
			if strings.Contains(text, "没有有效") {
				return text
			}
		case <-deadline:
			t.Fatal("未等到暂存广播 binding 拒绝回复")
		}
	}
}

func (r *synchronizedTextReplier) texts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.Texts...)
}
