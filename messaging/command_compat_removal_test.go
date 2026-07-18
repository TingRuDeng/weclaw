package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestRemovedCompatibilityRoutesAreNotBuiltinCommands(t *testing.T) {
	h := NewHandler(nil, nil)
	for _, trimmed := range []string{
		"/info",
		"/clear",
		"/sw reload",
		"/upgrade",
		"/codex ls",
		"/claude ls",
	} {
		handled := h.handleBuiltInPlatformCommand(context.Background(), platformCommandRequest{
			Message: platform.IncomingMessage{
				Platform: platform.PlatformWeChat,
				UserID:   "user-1",
				Text:     trimmed,
			},
			RouteUserID: "user-1",
			Reply:       platformtest.NewReplier(platform.Capabilities{Text: true}),
			Trimmed:     trimmed,
		})
		if handled {
			t.Fatalf("%q 不应再被内置命令消费", trimmed)
		}
	}
}

func TestRemovedModeAliasesAreRejected(t *testing.T) {
	h := NewHandler(nil, nil)
	userID := "user-1"
	h.setYoloMode(userID, true)

	for _, trimmed := range []string{"/mode ask", "/mode off"} {
		reply := h.handleModeCommand(userID, trimmed)
		if !strings.Contains(reply, "用法") {
			t.Fatalf("%q 回复=%q，期望返回用法", trimmed, reply)
		}
		if !h.isYoloMode(userID) {
			t.Fatalf("%q 不应改变 yolo 模式", trimmed)
		}
	}
}

func TestRemovedModelListAliasIsRejected(t *testing.T) {
	h := NewHandler(nil, nil)
	codex := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		models:    []agent.CodexModel{{ID: "gpt-5"}},
	}
	claude := &fakeClaudeModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}},
		models:    []agent.ClaudeModel{{ID: "claude-sonnet-5"}},
	}

	codexReply := h.handleCodexModelCommand(context.Background(), codex, []string{"list"})
	if !strings.Contains(codexReply, "用法") || strings.Contains(codexReply, "Codex 可用模型") {
		t.Fatalf("codex model list 回复=%q，期望返回用法", codexReply)
	}
	claudeReply := h.handleClaudeModelCommand(context.Background(), claude, []string{"list"})
	if !strings.Contains(claudeReply, "用法") || strings.Contains(claudeReply, "Claude 可用模型") {
		t.Fatalf("claude model list 回复=%q，期望返回用法", claudeReply)
	}
}

type fakeClaudeModelAgent struct {
	fakeAgent
	model  string
	effort string
	models []agent.ClaudeModel
}

func (f *fakeClaudeModelAgent) ClaudeModelStatus() agent.ClaudeModelStatus {
	return agent.ClaudeModelStatus{Model: f.model, Effort: f.effort}
}

func (f *fakeClaudeModelAgent) ListClaudeModels(context.Context) ([]agent.ClaudeModel, error) {
	return f.models, nil
}

func (f *fakeClaudeModelAgent) SetClaudeModel(model string, effort string) {
	if model != "" {
		f.model = model
	}
	if effort != "" {
		f.effort = effort
	}
}
