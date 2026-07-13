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
	assertModelCardMetadata(t, modelCard.Choices, sessionKey, "claude")

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
	assertModelCardMetadata(t, choices, sessionKey, "claude")
}

// assertModelCardMetadata 验证卡片回调仍绑定生成卡片时的飞书会话和 Agent。
func assertModelCardMetadata(t *testing.T, choices []platform.Choice, sessionKey string, agentName string) {
	t.Helper()
	for _, choice := range choices {
		if choice.Metadata[feishuSessionMetadataKey] != sessionKey {
			t.Fatalf("choice=%#v，期望保留飞书会话键 %q", choice, sessionKey)
		}
		if choice.Metadata[modelSettingAgentMetadataKey] != agentName {
			t.Fatalf("choice=%#v，期望保留目标 Agent %q", choice, agentName)
		}
	}
}

// TestFeishuModelCardValidatesOriginalAgent 验证模型卡只操作生成时的当前 Agent。
func TestFeishuModelCardValidatesOriginalAgent(t *testing.T) {
	tests := []struct {
		name, expectedAgent, currentAgent string
		wantStale                         bool
	}{
		{name: "当前 Agent 未变化", expectedAgent: "claude", currentAgent: "claude"},
		{name: "旧卡缺少 Agent", currentAgent: "claude", wantStale: true},
		{name: "当前 Agent 已切换", expectedAgent: "claude", currentAgent: "codex", wantStale: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, codex, claude, sessionKey := newModelCardGuardHandler(t, tt.currentAgent)
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			h.HandleMessage(context.Background(), platform.IncomingMessage{
				Platform: platform.PlatformFeishu, UserID: "user-1", MessageID: "model-card-" + tt.name,
				RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
					"choice": "/reasoning high", modelSettingAgentMetadataKey: tt.expectedAgent,
				}},
				Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
			}, reply)
			if tt.wantStale {
				if !containsText(reply.Texts, "卡片已失效") || codex.effort != "" || claude.effort != "medium" {
					t.Fatalf("texts=%#v codex=%q claude=%q，期望拒绝旧卡", reply.Texts, codex.effort, claude.effort)
				}
				return
			}
			if claude.effort != "high" || !containsText(reply.Texts, "已将 claude 推理强度切换为") {
				t.Fatalf("texts=%#v effort=%q，期望更新当前 Claude", reply.Texts, claude.effort)
			}
		})
	}
}

// newModelCardGuardHandler 构造可切换当前 Agent 的模型卡片测试环境。
func newModelCardGuardHandler(t *testing.T, currentAgent string) (*Handler, *fakeCodexModelAgent, *fakeClaudeModelAgent, string) {
	t.Helper()
	codex := &fakeCodexModelAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp"}}, model: "gpt-5"}
	claude := &fakeClaudeModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp"}},
		model:     "sonnet", effort: "medium", models: []agent.ClaudeModel{{ID: "sonnet", EffortOptions: []string{"medium", "high"}}},
	}
	h := newClaudeModelHandler(codex, claude)
	sessionKey := "feishu:tenant:dm:model-card:user-1"
	if err := h.ensureAgentSessions().Set(sessionKey, currentAgent); err != nil {
		t.Fatal(err)
	}
	return h, codex, claude, sessionKey
}

type fakeCurrentClaudeModelAgent struct {
	fakeClaudeModelAgent
	config  agent.ClaudeSessionConfig
	updates []agent.ClaudeSessionConfigUpdate
}

// ClaudeSessionConfig 返回当前测试 session 的运行时配置。
func (f *fakeCurrentClaudeModelAgent) ClaudeSessionConfig(string) (agent.ClaudeSessionConfig, bool) {
	return f.config, true
}

// SetClaudeSessionConfig 记录消息层实际发送给当前 session 的配置更新。
func (f *fakeCurrentClaudeModelAgent) SetClaudeSessionConfig(_ context.Context, update agent.ClaudeSessionConfigUpdate) error {
	f.updates = append(f.updates, update)
	if update.Model != "" {
		f.config.Model = update.Model
	}
	if update.Effort != "" {
		f.config.Effort = update.Effort
	}
	return nil
}

func TestFeishuClaudeSettingsUpdateCurrentSession(t *testing.T) {
	claude := &fakeCurrentClaudeModelAgent{fakeClaudeModelAgent: fakeClaudeModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp"}},
		model:     "default-model", effort: "medium",
		models: []agent.ClaudeModel{{ID: "opus", EffortOptions: []string{"medium", "high"}}},
	}, config: agent.ClaudeSessionConfig{Model: "sonnet", Effort: "medium"}}
	h := newClaudeModelHandler(&fakeCodexModelAgent{}, claude)
	sessionKey := "feishu:tenant:dm:chat-current:user-1"
	workspace := t.TempDir()
	if err := h.claudeSessions.commitSelection(claudeBindingKey(sessionKey, "claude"), workspace, "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.ensureAgentSessions().Set(sessionKey, "claude"); err != nil {
		t.Fatal(err)
	}
	for index, command := range []string{"/model opus", "/reasoning high"} {
		reply := handleModelCardMessage(t, h, modelCardTestRequest{sessionKey, command, fmt.Sprintf("current-%d", index)})
		if !containsText(reply.Texts, "当前 Claude session") {
			t.Fatalf("command=%q texts=%#v", command, reply.Texts)
		}
	}
	if len(claude.updates) != 2 || claude.config.Model != "opus" || claude.config.Effort != "high" {
		t.Fatalf("updates=%#v config=%#v", claude.updates, claude.config)
	}
	if claude.model != "default-model" {
		t.Fatalf("默认模型=%q，不应覆盖新会话默认值", claude.model)
	}
	card := handleModelCardMessage(t, h, modelCardTestRequest{sessionKey, "/model", "current-status"})
	if len(card.Choices) != 1 || len(card.Choices[0].Choices) != 1 ||
		!strings.Contains(card.Choices[0].Choices[0].Label, "当前") {
		t.Fatalf("当前 session 模型卡片=%#v，期望 opus 标记为当前", card.Choices)
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
