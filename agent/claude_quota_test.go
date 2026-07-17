package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseClaudeQuotaExtractsSubscriptionWindows(t *testing.T) {
	quota, err := parseClaudeQuota(json.RawMessage(`{
		"subscription_type":"max",
		"rate_limits_available":true,
		"rate_limits":{
			"five_hour":{"utilization":12.5,"resets_at":"2026-07-17T08:00:00Z"},
			"seven_day":{"utilization":40,"resets_at":"2026-07-20T08:00:00Z"},
			"seven_day_opus":null,
			"seven_day_sonnet":{"utilization":null,"resets_at":null},
			"model_scoped":[{"display_name":"Fable","utilization":66.6,"resets_at":"2026-07-21T08:00:00Z"}],
			"extra_usage":{"is_enabled":true,"monthly_limit":50,"used_credits":12.5,"utilization":25,"currency":"USD"}
		}
	}`))
	if err != nil {
		t.Fatalf("parseClaudeQuota error: %v", err)
	}
	if quota.SubscriptionType != "max" || !quota.RateLimitsAvailable {
		t.Fatalf("quota=%+v", quota)
	}
	if len(quota.Limits) != 4 {
		t.Fatalf("limits=%+v, want four non-null windows", quota.Limits)
	}
	if quota.Limits[0].ID != "five_hour" || quota.Limits[0].UsedPercent == nil || *quota.Limits[0].UsedPercent != 12.5 {
		t.Fatalf("five-hour limit=%+v", quota.Limits[0])
	}
	if quota.Limits[2].ID != "seven_day_sonnet" || quota.Limits[2].UsedPercent != nil {
		t.Fatalf("sonnet limit=%+v, want present window with unknown utilization", quota.Limits[2])
	}
	if quota.Limits[3].ID != "model_scoped" || quota.Limits[3].Name != "Fable" {
		t.Fatalf("model limit=%+v", quota.Limits[3])
	}
	if quota.ExtraUsage == nil || !quota.ExtraUsage.Enabled || quota.ExtraUsage.UsedPercent == nil || *quota.ExtraUsage.UsedPercent != 25 || quota.ExtraUsage.MonthlyLimit == nil || *quota.ExtraUsage.MonthlyLimit != 50 || quota.ExtraUsage.UsedCredits == nil || *quota.ExtraUsage.UsedCredits != 12.5 || quota.ExtraUsage.Currency != "USD" {
		t.Fatalf("extra usage=%+v", quota.ExtraUsage)
	}
}

func TestParseClaudeQuotaPreservesUnavailableState(t *testing.T) {
	quota, err := parseClaudeQuota(json.RawMessage(`{
		"subscription_type":null,
		"rate_limits_available":false,
		"rate_limits":null
	}`))
	if err != nil {
		t.Fatalf("parseClaudeQuota error: %v", err)
	}
	if quota.SubscriptionType != "" || quota.RateLimitsAvailable || len(quota.Limits) != 0 || quota.ExtraUsage != nil {
		t.Fatalf("quota=%+v", quota)
	}
}

func TestQueryClaudeQuotaUsesControlProtocolWithoutPrompt(t *testing.T) {
	responses := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"` + claudeQuotaInitializeID + `","response":{"commands":[]}}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"` + claudeQuotaReadRequestID + `","response":{"subscription_type":"pro","rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":7,"resets_at":"2026-07-17T08:00:00Z"}}}}}`,
	}, "\n") + "\n"
	var requests bytes.Buffer

	quota, err := queryClaudeQuota(&requests, strings.NewReader(responses))
	if err != nil {
		t.Fatalf("queryClaudeQuota error: %v", err)
	}
	if quota.SubscriptionType != "pro" || len(quota.Limits) != 1 || quota.Limits[0].ID != "five_hour" {
		t.Fatalf("quota=%+v", quota)
	}
	lines := strings.Split(strings.TrimSpace(requests.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("requests=%q, want initialize and get_usage only", requests.String())
	}
	var got []claudeControlRequest
	for _, line := range lines {
		var request claudeControlRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatalf("decode request %q: %v", line, err)
		}
		got = append(got, request)
	}
	if got[0].Request.Subtype != "initialize" || got[1].Request.Subtype != "get_usage" {
		t.Fatalf("requests=%+v", got)
	}
}

func TestQueryClaudeQuotaReturnsControlError(t *testing.T) {
	responses := strings.Join([]string{
		`{"type":"control_response","response":{"subtype":"success","request_id":"` + claudeQuotaInitializeID + `","response":{}}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"` + claudeQuotaReadRequestID + `","error":"get_usage is not supported"}}`,
	}, "\n") + "\n"

	_, err := queryClaudeQuota(&bytes.Buffer{}, strings.NewReader(responses))
	if err == nil || !strings.Contains(err.Error(), "control request is not supported") || strings.Contains(err.Error(), "get_usage") {
		t.Fatalf("error=%v", err)
	}
}

func TestNewClaudeQuotaCommandDisablesSessionAndProjectConfiguration(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude",
		Command:        "claude-agent-acp",
		LocalCommand:   "/usr/bin/claude",
		Cwd:            t.TempDir(),
		Env:            map[string]string{"WECLAW_CLAUDE_QUOTA_TEST": "enabled"},
	})
	cmd, err := a.newClaudeQuotaCommand(context.Background(), a.localCommand)
	if err != nil {
		t.Fatalf("newClaudeQuotaCommand error: %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--no-session-persistence", "--setting-sources", "--strict-mcp-config"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args=%q, want %q", joined, want)
		}
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"CLAUDE_CODE_ENTRYPOINT=cli", "WECLAW_CLAUDE_QUOTA_TEST=enabled"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q", want)
		}
	}
}

func TestReadClaudeQuotaRequiresNativeClaudeCommand(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{ConfiguredName: "claude", Command: "claude-agent-acp"})
	a.claudeQuotaOAuthToken = func(context.Context) (string, error) { return "", nil }
	_, err := a.ReadClaudeQuota(context.Background())
	if err == nil || !strings.Contains(err.Error(), "local_command") {
		t.Fatalf("error=%v", err)
	}
}

func TestReadClaudeQuotaUsesOAuthWithoutNativeCommand(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{ConfiguredName: "claude", Command: "claude-agent-acp"})
	a.claudeQuotaOAuthToken = func(context.Context) (string, error) { return "test-oauth-token", nil }
	a.claudeQuotaOAuthQuery = func(_ context.Context, token string) (ClaudeQuota, error) {
		if token != "test-oauth-token" {
			t.Fatalf("token=%q", token)
		}
		return ClaudeQuota{RateLimitsAvailable: true}, nil
	}

	quota, err := a.ReadClaudeQuota(context.Background())
	if err != nil {
		t.Fatalf("ReadClaudeQuota error: %v", err)
	}
	if !quota.RateLimitsAvailable {
		t.Fatalf("quota=%+v", quota)
	}
}
