package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
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
