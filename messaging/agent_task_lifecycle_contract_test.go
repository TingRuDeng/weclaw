package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

// TestSynchronousAgentQueuesOneMessage 验证同步 Agent 与后台 Agent 共享单条排队语义。
func TestSynchronousAgentQueuesOneMessage(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "custom", Type: "test", Command: "custom"}
	h.SetDefaultAgent("custom", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	request := func(message string) agentMessageRequest {
		return agentMessageRequest{
			ctx: context.Background(), platformName: platform.PlatformFeishu,
			userID: "user-1", routeUserID: "route-1", reply: reply,
			name: "custom", message: message,
		}
	}
	firstDone := make(chan struct{})
	go func() {
		h.sendToNamedAgent(request("第一条"))
		close(firstDone)
	}()
	waitForAgentEnter(t, ag)
	secondDone := make(chan struct{})
	go func() {
		h.sendToNamedAgent(request("第二条"))
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
		close(ag.release)
		t.Fatal("同步 Agent 的第二条消息未及时进入暂存队列")
	}
	if !containsText(reply.Texts, queuedAgentMessage) {
		close(ag.release)
		t.Fatalf("texts=%#v，缺少排队提示", reply.Texts)
	}
	ag.release <- struct{}{}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		close(ag.release)
		t.Fatal("同步 Agent 第一条消息未完成")
	}
	waitForAgentEnter(t, ag)
	ag.release <- struct{}{}
	key := h.agentExecutionKeyForRoute("user-1", "route-1", "custom", ag)
	waitUntil(t, func() bool {
		_, active := h.activeTask(key)
		return !active
	})
	started, maxActive := ag.stats()
	if started != 2 || maxActive != 1 {
		t.Fatalf("started=%d maxActive=%d，期望严格串行续跑", started, maxActive)
	}
}
