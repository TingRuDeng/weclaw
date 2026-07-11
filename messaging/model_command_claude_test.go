package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuModelCommandsUseSessionDefaultAgent(t *testing.T) {
	codex := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5",
	}
	claude := &fakeClaudeModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}},
		model:     "sonnet",
		effort:    "medium",
		models: []agent.ClaudeModel{{
			ID: "claude-sonnet-5", Alias: "sonnet", EffortOptions: []string{"low", "medium", "high"},
		}},
	}
	h := newClaudeModelHandler(codex, claude)
	sessionKey := "feishu:tenant:dm:chat-a:user-1"
	if err := h.ensureAgentSessions().Set(sessionKey, "claude"); err != nil {
		t.Fatalf("设置会话 Agent 失败：%v", err)
	}

	for index, command := range []string{"/model opus", "/reasoning high"} {
		reply := platformtest.NewReplier(platform.Capabilities{Text: true})
		h.HandleMessage(context.Background(), platform.IncomingMessage{
			Platform: platform.PlatformFeishu, AccountID: "cli_main", UserID: "user-1",
			MessageID: fmt.Sprintf("model-session-%d", index), Text: command,
			Metadata: map[string]string{"feishu_session_key": sessionKey},
		}, reply)
		if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "claude") {
			t.Fatalf("command=%q reply=%#v，期望操作当前会话的 claude", command, reply.Texts)
		}
	}
	if claude.model != "opus" || claude.effort != "high" {
		t.Fatalf("Claude 配置=(%q,%q)，期望 (opus,high)", claude.model, claude.effort)
	}
	if codex.model != "gpt-5" || codex.effort != "" {
		t.Fatalf("Codex 配置=(%q,%q)，不应被 Claude 会话命令修改", codex.model, codex.effort)
	}
}

func TestFeishuClaudeModelAndReasoningCommandsUseChoiceCards(t *testing.T) {
	codex := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5.6-sol",
		models:    []agent.CodexModel{{ID: "gpt-5.6-sol", EffortOptions: []string{"medium", "high"}}},
	}
	claude := &fakeClaudeModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}},
		model:     "sonnet",
		effort:    "medium",
		models: []agent.ClaudeModel{
			{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Alias: "sonnet", EffortOptions: []string{"low", "medium", "high"}},
			{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", Alias: "opus", EffortOptions: []string{"low", "high"}},
		},
	}
	h := newClaudeModelHandler(codex, claude)
	sessionKey := "feishu:tenant:dm:chat-claude:user-1"
	if err := h.ensureAgentSessions().Set(sessionKey, "claude"); err != nil {
		t.Fatalf("设置会话 Agent 失败：%v", err)
	}

	modelReply := handleModelCardMessage(t, h, modelCardTestRequest{sessionKey, "/model", "claude-model-card"})
	if len(modelReply.Choices) != 1 || len(modelReply.Choices[0].Choices) != 2 {
		t.Fatalf("model choices=%#v，期望 Claude 模型卡片", modelReply.Choices)
	}
	modelCard := modelReply.Choices[0]
	if !strings.Contains(modelCard.Prompt, "Claude") || modelCard.Choices[0].ID != "/model claude-sonnet-5" || !strings.Contains(modelCard.Choices[0].Label, "当前") {
		t.Fatalf("model card=%#v，期望 Claude 当前模型和切换命令", modelCard)
	}

	reasoningReply := handleModelCardMessage(t, h, modelCardTestRequest{sessionKey, "/reasoning", "claude-reasoning-card"})
	if len(reasoningReply.Choices) != 1 {
		t.Fatalf("reasoning choices=%#v，期望 Claude 推理强度卡片", reasoningReply.Choices)
	}
	wantIDs := []string{"/reasoning low", "/reasoning medium", "/reasoning high"}
	choices := reasoningReply.Choices[0].Choices
	if len(choices) != len(wantIDs) {
		t.Fatalf("reasoning choices=%#v，期望当前 Claude 模型支持的档位", choices)
	}
	for index, want := range wantIDs {
		if choices[index].ID != want {
			t.Fatalf("choices[%d].ID=%q，期望 %q", index, choices[index].ID, want)
		}
	}
}

func newClaudeModelHandler(codex agent.Agent, claude agent.Agent) *Handler {
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "claude" {
			return claude
		}
		return codex
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetAgentMetas([]AgentMeta{{Name: "claude"}, {Name: "codex"}})
	h.SetPlatformDefaultAgents(map[string]string{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_main"): "codex",
	})
	return h
}

type modelCardTestRequest struct {
	sessionKey string
	command    string
	messageID  string
}

func handleModelCardMessage(t *testing.T, h *Handler, request modelCardTestRequest) *platformtest.Replier {
	t.Helper()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "user-1", MessageID: request.messageID, Text: request.command,
		Metadata: map[string]string{"feishu_session_key": request.sessionKey},
	}, reply)
	return reply
}
