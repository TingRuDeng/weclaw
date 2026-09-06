package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestUnknownSlashCommandNeverInvokesAgent(t *testing.T) {
	for _, input := range []string{"/typo", "/typo secret", "/tmp", "/mock /typo secret"} {
		t.Run(input, func(t *testing.T) {
			ag := &fakeAgent{reply: "executed"}
			h := NewHandler(nil, nil)
			h.SetDefaultAgent("mock", ag)
			h.SetAgentMetas([]AgentMeta{{Name: "mock"}})
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			h.handleMessageForTest(context.Background(), platform.IncomingMessage{Platform: platform.PlatformWeChat, UserID: "user", Text: input}, reply)
			if ag.wasChatCalled() || h.ActiveTaskCount() != 0 || !containsText(reply.Texts, "命令不存在") {
				t.Fatalf("called=%v active=%d replies=%v", ag.wasChatCalled(), h.ActiveTaskCount(), reply.Texts)
			}
		})
	}
}

func TestSlashCommandPreservesPathsAndAliases(t *testing.T) {
	for _, input := range []string{"/tmp/", "/tmp/file", "查看 /tmp", "/mock hello", "/ai hello", "/cc status foo"} {
		t.Run(input, func(t *testing.T) {
			ag := &fakeAgent{reply: "executed", info: agent.AgentInfo{Name: "mock", Type: "test"}}
			h := NewHandler(nil, nil)
			h.SetDefaultAgent("mock", ag)
			h.agents["claude"] = ag
			h.SetAgentMetas([]AgentMeta{{Name: "mock"}, {Name: "claude"}})
			h.customAliases = map[string]string{"ai": "mock"}
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			h.handleMessageForTest(context.Background(), platform.IncomingMessage{Platform: platform.PlatformWeChat, UserID: "user", Text: input}, reply)
			if !ag.wasChatCalled() || strings.Contains(strings.Join(reply.Texts, ""), "命令不存在") {
				t.Fatalf("called=%v replies=%v", ag.wasChatCalled(), reply.Texts)
			}
		})
	}
}

func TestUnavailableDefaultAgentDoesNotEcho(t *testing.T) {
	for _, name := range []string{"", "missing"} {
		t.Run(name, func(t *testing.T) {
			h := NewHandler(nil, nil)
			h.defaultName = name
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			h.sendToDefaultAgent(agentMessageRequest{ctx: context.Background(), userID: "user", routeUserID: "user", reply: reply, message: "private-original-input"})
			want := "未配置默认 Agent"
			if name != "" {
				want = "Agent 启动失败"
			}
			if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], want) || strings.Contains(reply.Texts[0], "private-original-input") || strings.Contains(reply.Texts[0], "[echo]") {
				t.Fatalf("replies=%v, want %q without echo", reply.Texts, want)
			}
		})
	}
}
