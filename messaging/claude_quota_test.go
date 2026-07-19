package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeClaudeQuotaAgent struct {
	fakeAgent
	quota    agent.ClaudeQuota
	quotaErr error
}

func (f *fakeClaudeQuotaAgent) ReadClaudeQuota(context.Context) (agent.ClaudeQuota, error) {
	if f.quotaErr != nil {
		return agent.ClaudeQuota{}, f.quotaErr
	}
	return f.quota, nil
}

func TestHandleClaudeQuotaCommandShowsSubscriptionLimits(t *testing.T) {
	fiveHour := 12.5
	weekly := 40.0
	model := 66.6
	extra := 25.0
	monthlyLimit := 50.0
	usedCredits := 12.5
	h := NewHandler(nil, nil)
	ag := &fakeClaudeQuotaAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}},
		quota: agent.ClaudeQuota{
			SubscriptionType:    "max",
			RateLimitsAvailable: true,
			Limits: []agent.ClaudeRateLimit{
				{ID: "five_hour", UsedPercent: &fiveHour, ResetsAt: "2026-07-17T08:00:00Z"},
				{ID: "seven_day", UsedPercent: &weekly},
				{ID: "model_scoped", Name: "Fable", UsedPercent: &model},
			},
			ExtraUsage: &agent.ClaudeExtraUsage{
				Enabled: true, UsedPercent: &extra, MonthlyLimit: &monthlyLimit, UsedCredits: &usedCredits, Currency: "USD",
			},
		},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetAgentWorkDirs(map[string]string{"claude": t.TempDir()})

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc quota")
	for _, want := range []string{
		"Claude 账号额度", "plan: max", "5 小时: 已用 12.5%", "7 天（全部模型）: 已用 40%",
		"Fable: 已用 66.6%", "额外用量: 已启用，额度 12.5 / 50 USD，已用 25%", "重置",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("quota text=%q, want %q", text, want)
		}
	}
}

func TestHandleClaudeQuotaCommandExplainsUnavailableSubscriptionLimits(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeClaudeQuotaAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}},
		quota:     agent.ClaudeQuota{RateLimitsAvailable: false},
	}
	h.defaultName = "claude"
	h.agents["claude"] = ag

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc quota")
	if !strings.Contains(text, "未提供 Claude 订阅额度") || !strings.Contains(text, "API key") {
		t.Fatalf("quota text=%q", text)
	}
}

func TestHandleClaudeQuotaCommandReportsUnsupportedAndQueryErrors(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		h := NewHandler(nil, nil)
		h.defaultName = "claude"
		h.agents["claude"] = &fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}
		text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc quota")
		if !strings.Contains(text, "不支持额度查询") {
			t.Fatalf("text=%q", text)
		}
	})

	t.Run("query error", func(t *testing.T) {
		h := NewHandler(nil, nil)
		h.defaultName = "claude"
		h.agents["claude"] = &fakeClaudeQuotaAgent{
			fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}},
			quotaErr:  errors.New("get_usage is not supported"),
		}
		text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc quota")
		if !strings.Contains(text, "查询 Claude 额度失败") || !strings.Contains(text, "Claude Code 已登录") || strings.Contains(text, "get_usage is not supported") {
			t.Fatalf("text=%q", text)
		}
	})
}

func TestClaudeQuotaCommandDetectionAndHelp(t *testing.T) {
	if !isClaudeSessionCommand("/cc quota") {
		t.Fatal("/cc quota should be a Claude session command")
	}
	if !strings.Contains(buildClaudeSessionHelpText(), "/cc quota 查看 Claude 账号额度") {
		t.Fatalf("Claude help=%q", buildClaudeSessionHelpText())
	}
}

func TestClaudeSessionCommandDetectionPreservesReservedWordPrompts(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: "/cc status", want: true},
		{command: "/cc status explain this", want: false},
		{command: "/cc new", want: true},
		{command: "/cc new session naming", want: false},
		{command: "/cc cd", want: true},
		{command: "/cc cd 2", want: true},
		{command: "/cc cd this workspace", want: false},
		{command: "/cc owner", want: true},
		{command: "/cc owner remote", want: true},
		{command: "/cc owner REMOTE", want: true},
		{command: "/cc owner of this file", want: false},
		{command: "/cc model", want: true},
		{command: "/cc model status", want: true},
		{command: "/cc model this package", want: false},
		{command: "/cc help", want: true},
		{command: "/cc help me", want: false},
		{command: "/cc page sessions 2", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := isClaudeSessionCommand(tt.command); got != tt.want {
				t.Fatalf("isClaudeSessionCommand(%q)=%t, want %t", tt.command, got, tt.want)
			}
		})
	}

	h := newTestHandler()
	names, message := h.parseCommand("/cc status explain this")
	if len(names) != 1 || names[0] != "claude" || message != "status explain this" {
		t.Fatalf("reserved-word prompt route=(%v,%q), want Claude message", names, message)
	}
}
