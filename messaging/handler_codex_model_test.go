package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleCodexModelStatusCommandShowsCurrentConfig(t *testing.T) {
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		t.Fatalf("model status should not start agent %q", name)
		return nil
	}, nil)
	h.SetAgentMetas([]AgentMeta{{
		Name:    "codex",
		Type:    "acp",
		Command: "codex",
		Model:   "gpt-5.4",
		Effort:  "high",
	}})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(121, "/cx model status"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Codex 新会话默认模型配置") ||
		!strings.Contains(text, "model: gpt-5.4") ||
		!strings.Contains(text, "effort: high") {
		t.Fatalf("model status reply mismatch, messages=%#v", calls.texts())
	}
}

func TestHandleCodexModelLsCommandListsModelsAndEfforts(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		models: []agent.CodexModel{
			{ID: "gpt-5.4", Name: "GPT-5.4", EffortOptions: []string{"medium", "high"}},
			{ID: "gpt-5.3-codex", EffortOptions: []string{"low", "medium"}},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": t.TempDir()})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(122, "/cx model ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Codex 可用模型") ||
		!strings.Contains(text, "0. gpt-5.4 (GPT-5.4)") ||
		!strings.Contains(text, "effort: medium, high") ||
		!strings.Contains(text, "1. gpt-5.3-codex") {
		t.Fatalf("model ls reply mismatch, messages=%#v", calls.texts())
	}
}

func TestHandleCodexQuotaCommandShowsRateLimits(t *testing.T) {
	reset := int64(1710003600)
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		quota: agent.CodexQuota{Limits: []agent.CodexRateLimit{
			{
				ID:       "codex",
				Name:     "Codex",
				PlanType: "pro",
				Primary: &agent.CodexRateLimitWindow{
					UsedPercent:        80,
					ResetsAt:           &reset,
					WindowDurationMins: int64Ptr(300),
				},
				Secondary: &agent.CodexRateLimitWindow{UsedPercent: 20},
				Credits:   &agent.CodexCredits{Balance: "10", HasCredits: true},
			},
			{
				ID:          "research",
				ReachedType: "rate_limit_reached",
				Primary:     &agent.CodexRateLimitWindow{UsedPercent: 100},
			},
		}},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": t.TempDir()})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(132, "/cx quota"))

	text := strings.Join(calls.texts(), "\n")
	for _, want := range []string{
		"Codex 账号额度",
		"codex (Codex)",
		"plan: pro",
		"primary: 已用 80%",
		"secondary: 已用 20%",
		"credits: 有额度，余额 10",
		"research",
		"已达到限制: rate_limit_reached",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("quota reply missing %q, messages=%#v", want, calls.texts())
		}
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}
