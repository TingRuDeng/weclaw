package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestACPAgentListCodexModelsParsesEffortOptions(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "model/list" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		return json.RawMessage(`{"data":[{"id":"gpt-5.4","displayName":"GPT-5.4","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"medium"},{"reasoningEffort":"high"}],"serviceTiers":[{"id":"priority","name":"Fast","description":"increased speed"}],"defaultServiceTier":"default"},{"id":"gpt-5.3-codex","effortOptions":["low","medium"],"additionalSpeedTiers":["fast"]}]}`), nil
	}

	models, err := a.ListCodexModels(context.Background())
	if err != nil {
		t.Fatalf("ListCodexModels error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models len=%d, want 2", len(models))
	}
	if models[0].ID != "gpt-5.4" || strings.Join(models[0].EffortOptions, ",") != "medium,high" {
		t.Fatalf("first model=%#v", models[0])
	}
	if !models[0].Default || len(models[0].ServiceTiers) != 1 ||
		models[0].ServiceTiers[0].ID != CodexServiceTierFast ||
		models[0].ServiceTiers[0].Name != "Fast" {
		t.Fatalf("first model service tiers=%#v", models[0])
	}
	if models[1].ID != "gpt-5.3-codex" || strings.Join(models[1].EffortOptions, ",") != "low,medium" {
		t.Fatalf("second model=%#v", models[1])
	}
	if len(models[1].ServiceTiers) != 1 || models[1].ServiceTiers[0].ID != CodexServiceTierFast {
		t.Fatalf("legacy service tiers=%#v", models[1].ServiceTiers)
	}
}

func TestACPAgentReadCodexQuotaParsesRateLimits(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "account/rateLimits/read" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		raw, ok := params.(json.RawMessage)
		if !ok || string(raw) != "null" {
			return nil, fmt.Errorf("params=%#v, want JSON null", params)
		}
		return sampleCodexQuotaResponse(), nil
	}

	quota, err := a.ReadCodexQuota(context.Background())
	if err != nil {
		t.Fatalf("ReadCodexQuota error: %v", err)
	}
	if len(quota.Limits) != 2 {
		t.Fatalf("limits len=%d, want 2", len(quota.Limits))
	}
	first := quota.Limits[0]
	if first.ID != "codex" || first.Name != "Codex" || first.PlanType != "pro" {
		t.Fatalf("first limit metadata=%#v", first)
	}
	if first.Primary == nil || first.Primary.UsedPercent != 80 || first.Primary.ResetsAt == nil || *first.Primary.ResetsAt != 1710003600 {
		t.Fatalf("first primary=%#v", first.Primary)
	}
	if first.Secondary == nil || first.Secondary.UsedPercent != 20 {
		t.Fatalf("first secondary=%#v", first.Secondary)
	}
	if first.Credits == nil || first.Credits.Balance != "10" || !first.Credits.HasCredits || first.Credits.Unlimited {
		t.Fatalf("first credits=%#v", first.Credits)
	}
	if quota.Limits[1].ID != "research" || quota.Limits[1].ReachedType != "rate_limit_reached" {
		t.Fatalf("second limit=%#v", quota.Limits[1])
	}
}

func sampleCodexQuotaResponse() json.RawMessage {
	return json.RawMessage(`{
		"rateLimits": {"limitId": "legacy", "limitName": "Legacy", "primary": {"usedPercent": 10}},
		"rateLimitsByLimitId": {
			"codex": {
				"limitId": "codex",
				"limitName": "Codex",
				"planType": "pro",
				"primary": {"usedPercent": 80, "resetsAt": 1710003600, "windowDurationMins": 300},
				"secondary": {"usedPercent": 20},
				"credits": {"balance": "10", "hasCredits": true, "unlimited": false}
			},
			"research": {
				"limitId": "research",
				"limitName": "Research",
				"rateLimitReachedType": "rate_limit_reached",
				"primary": {"usedPercent": 100}
			}
		}
	}`)
}
