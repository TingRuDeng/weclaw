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
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", reply, "claude", "第一条", "client-1")
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)

	secondDone := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-2")
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", reply, "claude", "第二条", "client-2")
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
		h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", reply, "slow", "hello", "client-1")
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
		h.sendToDefaultAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", reply, "hello", "client-1")
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
		h.broadcastToAgents(ctx, platform.PlatformWeChat, "user-1", "user-1", reply, []string{"slow"}, "hello")
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

	go h.sendToNamedAgent(ctx, platform.PlatformWeChat, "user-1", "user-1", wechat.NewReplier(client, "user-1", "ctx-1", "client-1"), "codex", "第一条", "client-1")
	waitForAgentEnter(t, codex)

	done := make(chan struct{})
	go func() {
		reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-2")
		h.broadcastToAgents(ctx, platform.PlatformWeChat, "user-1", "user-1", reply, []string{"codex", "claude"}, "第二条")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("broadcast should not block behind running Codex task")
	}
	waitForText(t, calls, "Codex 正在处理上一条任务")
	waitForText(t, calls, "[claude] claude ok")

	codex.release <- struct{}{}
}
