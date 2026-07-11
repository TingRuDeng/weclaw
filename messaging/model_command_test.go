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

// fakeCodexModelAgent 实现 CodexModelControlAgent，用于测试 /model /reasoning。
type fakeCodexModelAgent struct {
	fakeAgent
	model  string
	effort string
	models []agent.CodexModel
}

func (f *fakeCodexModelAgent) CodexModelStatus() agent.CodexModelStatus {
	return agent.CodexModelStatus{Model: f.model, Effort: f.effort}
}
func (f *fakeCodexModelAgent) ListCodexModels(context.Context) ([]agent.CodexModel, error) {
	return f.models, nil
}
func (f *fakeCodexModelAgent) SetCodexModel(model, effort string) {
	if model != "" {
		f.model = model
	}
	if effort != "" {
		f.effort = effort
	}
}

func newModelHandler(ag agent.Agent) *Handler {
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("codex", ag)
	return h
}

func TestModelCommandShowsAndSwitches(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5",
		models:    []agent.CodexModel{{ID: "gpt-5", EffortOptions: []string{"low", "high"}}, {ID: "gpt-5-codex"}},
	}
	h := newModelHandler(ag)

	overview := h.handleModelCommand(context.Background(), platform.PlatformWeChat, "")
	if !strings.Contains(overview, "gpt-5") || !strings.Contains(overview, "可用模型") {
		t.Fatalf("overview should list models: %q", overview)
	}
	out := h.handleModelCommand(context.Background(), platform.PlatformWeChat, "gpt-5-codex")
	if !strings.Contains(out, "gpt-5-codex") || ag.model != "gpt-5-codex" {
		t.Fatalf("model not switched: out=%q model=%q", out, ag.model)
	}
}

func TestReasoningCommandShowsAndSwitches(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5",
		effort:    "medium",
		models:    []agent.CodexModel{{ID: "gpt-5", EffortOptions: []string{"low", "medium", "high"}}},
	}
	h := newModelHandler(ag)

	overview := h.handleReasoningCommand(context.Background(), platform.PlatformWeChat, "")
	if !strings.Contains(overview, "medium") || !strings.Contains(overview, "可选") {
		t.Fatalf("reasoning overview should show options: %q", overview)
	}
	out := h.handleReasoningCommand(context.Background(), platform.PlatformWeChat, "high")
	if !strings.Contains(out, "high") || ag.effort != "high" {
		t.Fatalf("effort not switched: out=%q effort=%q", out, ag.effort)
	}
}

func TestModelCommandNonCodexAgentInforms(t *testing.T) {
	ag := &fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}}
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("claude", ag)
	out := h.handleModelCommand(context.Background(), platform.PlatformWeChat, "opus")
	if !strings.Contains(out, "由配置固定") {
		t.Fatalf("non-codex agent should report fixed-by-config: %q", out)
	}
}

func TestFeishuModelCommandsUseSessionDefaultAgent(t *testing.T) {
	codex := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5",
	}
	claude := &fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}}
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
	sessionKey := "feishu:tenant:dm:chat-a:user-1"
	if err := h.ensureAgentSessions().Set(sessionKey, "claude"); err != nil {
		t.Fatalf("设置会话 Agent 失败：%v", err)
	}

	for index, command := range []string{"/model opus", "/reasoning high"} {
		reply := platformtest.NewReplier(platform.Capabilities{Text: true})
		h.HandleMessage(context.Background(), platform.IncomingMessage{
			Platform:  platform.PlatformFeishu,
			AccountID: "cli_main",
			UserID:    "user-1",
			MessageID: fmt.Sprintf("model-session-%d", index),
			Text:      command,
			Metadata:  map[string]string{"feishu_session_key": sessionKey},
		}, reply)
		if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "claude") {
			t.Fatalf("command=%q reply=%#v，期望操作当前会话的 claude", command, reply.Texts)
		}
	}
}

func TestFeishuModelCommandUsesChoiceCard(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5.6-sol",
		models: []agent.CodexModel{
			{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"},
			{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra"},
		},
	}
	h := newModelHandler(ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_main",
		UserID:    "ou_user",
		MessageID: "model-card",
		Text:      "/model",
	}, reply)

	if len(reply.Texts) != 0 || len(reply.Choices) != 1 {
		t.Fatalf("texts=%#v choices=%#v，期望只发送一张模型卡片", reply.Texts, reply.Choices)
	}
	card := reply.Choices[0]
	if !strings.Contains(card.Prompt, "当前模型: gpt-5.6-sol") {
		t.Fatalf("prompt=%q，期望显示当前模型", card.Prompt)
	}
	if len(card.Choices) != 2 || card.Choices[0].ID != "/model gpt-5.6-sol" || card.Choices[1].ID != "/model gpt-5.6-terra" {
		t.Fatalf("choices=%#v，期望使用模型切换命令", card.Choices)
	}
	if !strings.Contains(card.Choices[0].Label, "当前") || strings.Contains(card.Choices[1].Label, "当前") {
		t.Fatalf("choices=%#v，期望只标记当前模型", card.Choices)
	}
}

func TestFeishuReasoningCommandUsesCurrentModelEffortChoices(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5.6-sol",
		effort:    "medium",
		models: []agent.CodexModel{
			{ID: "gpt-5.6-sol", EffortOptions: []string{"low", "medium", "high"}},
			{ID: "gpt-5.6-terra", EffortOptions: []string{"low", "high"}},
		},
	}
	h := newModelHandler(ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_main",
		UserID:    "ou_user",
		MessageID: "reasoning-card",
		Text:      "/reasoning",
	}, reply)

	if len(reply.Texts) != 0 || len(reply.Choices) != 1 {
		t.Fatalf("texts=%#v choices=%#v，期望只发送一张推理强度卡片", reply.Texts, reply.Choices)
	}
	card := reply.Choices[0]
	if !strings.Contains(card.Prompt, "当前推理强度: medium") {
		t.Fatalf("prompt=%q，期望显示当前推理强度", card.Prompt)
	}
	wantIDs := []string{"/reasoning low", "/reasoning medium", "/reasoning high"}
	if len(card.Choices) != len(wantIDs) {
		t.Fatalf("choices=%#v，期望只展示当前模型支持的强度", card.Choices)
	}
	for index, want := range wantIDs {
		if card.Choices[index].ID != want {
			t.Fatalf("choices[%d].ID=%q，期望 %q", index, card.Choices[index].ID, want)
		}
	}
	if !strings.Contains(card.Choices[1].Label, "当前") {
		t.Fatalf("choices=%#v，期望标记当前推理强度", card.Choices)
	}
}

func TestFeishuModelCommandFallsBackToTextWithoutChoices(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5.6-sol",
	}
	h := newModelHandler(ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_main",
		UserID:    "ou_user",
		MessageID: "model-card-fallback",
		Text:      "/model",
	}, reply)

	if len(reply.Choices) != 0 || len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "当前模型: gpt-5.6-sol") {
		t.Fatalf("texts=%#v choices=%#v，期望回退为模型文本概览", reply.Texts, reply.Choices)
	}
}

func TestExplicitReasoningCommandAndWechatOverviewStayText(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "gpt-5.6-sol",
		effort:    "medium",
		models:    []agent.CodexModel{{ID: "gpt-5.6-sol", EffortOptions: []string{"low", "medium", "high"}}},
	}
	h := newModelHandler(ag)
	feishuReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "reasoning-explicit",
		Text:      "/reasoning high",
	}, feishuReply)

	if len(feishuReply.Choices) != 0 || len(feishuReply.Texts) != 1 || ag.effort != "high" {
		t.Fatalf("texts=%#v choices=%#v effort=%q，显式命令应保持文本确认", feishuReply.Texts, feishuReply.Choices, ag.effort)
	}
	wechatReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		UserID:    "wx_user",
		MessageID: "reasoning-wechat",
		Text:      "/reasoning",
	}, wechatReply)
	if len(wechatReply.Choices) != 0 || len(wechatReply.Texts) != 1 {
		t.Fatalf("texts=%#v choices=%#v，微信概览应保持文本", wechatReply.Texts, wechatReply.Choices)
	}
}

func TestReasoningSettingCardRejectsEffortsFromDifferentModel(t *testing.T) {
	ag := &fakeCodexModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		model:     "custom-model",
		models:    []agent.CodexModel{{ID: "gpt-5.6-sol", EffortOptions: []string{"low", "medium", "high"}}},
	}

	_, choices := modelSettingCard(context.Background(), ag, modelSettingReasoning)

	if len(choices) != 0 {
		t.Fatalf("choices=%#v，未知当前模型不能借用其他模型的推理强度", choices)
	}
}
