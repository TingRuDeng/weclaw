package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestSendToNamedAgentSerializesSameExecutionKey(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}
	h.agents["claude"] = ag
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
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "claude", message: "第一条", clientID: "client-1"})
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	secondDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-2")
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "claude", message: "第二条", clientID: "client-2"})
		close(secondDone)
	}()
	time.Sleep(50 * time.Millisecond)
	started, maxActive := ag.stats()
	if started != 1 || maxActive != 1 {
		t.Fatalf("并发进入 Codex: started=%d maxActive=%d", started, maxActive)
	}

	ag.release <- struct{}{}
	waitDone(t, firstDone, "第一条任务")
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	waitDone(t, secondDone, "第二条任务")

	texts := calls.texts()
	firstIndex := textIndex(texts, "第1条结果")
	secondIndex := textIndex(texts, "第2条结果")
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("回复顺序错误，messages=%#v", texts)
	}
}

// TestSendToNamedAgentTracksNonCodexActiveTask 验证 Claude 等任务会阻止普通重启。
func TestSendToNamedAgentTracksNonCodexActiveTask(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}
	h.agents["claude"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "claude", message: "任务", clientID: "client-1"})
		close(done)
	}()
	waitForAgentEnter(t, ag)
	if got := h.ActiveTaskCount(); got != 1 {
		t.Fatalf("ActiveTaskCount()=%d, want 1 while Claude is running", got)
	}

	ag.release <- struct{}{}
	waitDone(t, done, "Claude 任务")
	waitForNoActiveTask(t, h, "user-1", ag)
	if got := h.ActiveTaskCount(); got != 0 {
		t.Fatalf("ActiveTaskCount()=%d, want 0 after Claude completed", got)
	}
}

func TestAgentExecutionLockRemovedAfterUse(t *testing.T) {
	h := NewHandler(nil, nil)
	unlock := h.lockAgentExecution("execution-1")
	unlock()

	h.taskLocksMu.Lock()
	defer h.taskLocksMu.Unlock()
	if _, ok := h.taskLocks["execution-1"]; ok {
		t.Fatal("idle execution lock was not removed")
	}
}

func TestSendToNamedAgentUsesTaskTimeout(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.agents["slow"] = ag
	h.SetProgressConfig(progressConfigWithTaskTimeout())

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "slow", message: "hello", clientID: "client-1"})
	})
	waitForText(t, calls, "本轮执行超时已被中止")
}

func TestSendToDefaultAgentUsesTaskTimeout(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.SetDefaultAgent("slow", ag)
	h.SetProgressConfig(progressConfigWithTaskTimeout())

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.sendToDefaultAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, message: "hello", clientID: "client-1"})
	})
	waitForText(t, calls, "本轮执行超时已被中止")
}

func TestBroadcastToAgentsUsesTaskTimeout(t *testing.T) {
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.agents["slow"] = ag
	h.SetProgressConfig(progressConfigWithTaskTimeout())

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
		h.broadcastToAgents(broadcastAgentsRequest{
			ctx:          ctx,
			platformName: platform.PlatformWeChat,
			userID:       "user-1",
			routeUserID:  "user-1",
			replyWriter:  reply,
			names:        []string{"slow"},
			message:      "hello",
		})
	})
	waitForText(t, calls, "本轮执行超时已被中止")
}

func TestBroadcastToRunningCodexReturnsGuideWithoutBlockingOtherAgents(t *testing.T) {
	h := NewHandler(nil, nil)
	codex := newBlockingProgressAgent()
	h.agents["codex"] = codex
	h.agents["claude"] = &fakeAgent{
		reply: "claude ok",
		info:  agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go h.sendToNamedAgent(agentMessageRequest{ctx: ctx, platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), name: "codex", message: "第一条", clientID: "client-1"})
	waitForAgentEnter(t, codex)

	done := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-2")
		h.broadcastToAgents(broadcastAgentsRequest{
			ctx:          ctx,
			platformName: platform.PlatformWeChat,
			userID:       "user-1",
			routeUserID:  "user-1",
			replyWriter:  reply,
			names:        []string{"codex", "claude"},
			message:      "第二条",
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("broadcast should not block behind running Codex task")
	}
	waitForText(t, calls, queuedAgentMessage)
	waitForText(t, calls, "[claude] claude ok")

	codex.release <- struct{}{}
	waitForAgentEnter(t, codex)
	codex.release <- struct{}{}
	waitForText(t, calls, "第2条结果")
	if containsText(calls.texts(), "回复“确认”执行该消息") {
		t.Fatalf("广播暂存消息自动续跑时不应要求确认，messages=%#v", calls.texts())
	}
}

func TestStatusCommandDoesNotWaitForOnDemandAgentStart(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		close(started)
		<-release
		return &fakeAgent{
			reply: "slow ok",
			info:  agent.AgentInfo{Name: name, Type: "cli", Command: name},
		}
	}, nil)
	h.SetAgentMetas([]AgentMeta{{Name: "slow", Type: "cli", Command: "slow"}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go h.HandleMessage(ctx, platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "user-1",
		Text:     "/slow hello",
	}, newAdminCommandTestReplier())
	select {
	case <-started:
	case <-time.After(taskWaitTimeout):
		t.Fatal("未等到按需 Agent 开始启动")
	}

	statusReply := newAdminCommandTestReplier()
	done := make(chan struct{})
	go func() {
		h.HandleMessage(ctx, platform.IncomingMessage{
			Platform: platform.PlatformWeChat,
			UserID:   "user-1",
			Text:     "/status",
		}, statusReply)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(150 * time.Millisecond):
		close(release)
		t.Fatal("/status 不应等待慢速 Agent 启动完成")
	}
	close(release)
	texts := statusReply.waitTexts(t, 1)
	if len(texts) != 1 || !containsText(texts, "WeClaw 运行态") {
		t.Fatalf("status texts=%#v, want runtime status", texts)
	}
}
