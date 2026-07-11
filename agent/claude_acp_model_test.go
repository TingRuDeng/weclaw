package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const claudeACPConfigOptionsJSON = `[
  {"id":"model","currentValue":"sonnet","options":[
    {"value":"sonnet","name":"Sonnet","description":"Balanced"},
    {"value":"opus","name":"Opus","description":"Complex"}
  ]},
  {"id":"effort","currentValue":"medium","options":[
    {"value":"low","name":"Low"},
    {"value":"medium","name":"Medium"},
    {"value":"high","name":"High"}
  ]}
]`

func TestClaudeACPConfiguresNewSessionModelThenEffort(t *testing.T) {
	ag := NewACPAgent(ACPAgentConfig{
		Command: "claude-agent-acp", Model: "opus", Effort: "high",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	var calls []string
	ag.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls = append(calls, method+":"+configOptionValue(params))
		switch method {
		case "session/new":
			return json.RawMessage(`{"sessionId":"session-1","configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
		case "session/set_config_option":
			return json.RawMessage(`{"configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	sessionID, isNew, err := ag.getOrCreateSession(context.Background(), "conversation-1")

	if err != nil || !isNew || sessionID != "session-1" {
		t.Fatalf("session=(%q,%v,%v)，期望新 session-1", sessionID, isNew, err)
	}
	want := []string{"session/new:", "session/set_config_option:opus", "session/set_config_option:high"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%#v，期望 %#v", calls, want)
	}
	models, err := ag.ListClaudeModels(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "claude-sonnet-5" || models[0].Alias != "sonnet" {
		t.Fatalf("models=%#v err=%v，期望缓存 ACP 模型选项", models, err)
	}
}

func TestClaudeACPDoesNotReconfigureExistingSession(t *testing.T) {
	ag := NewACPAgent(ACPAgentConfig{
		Command: "claude-agent-acp", StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	calls := 0
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		calls++
		if method != "session/new" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return json.RawMessage(`{"sessionId":"session-1","configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
	}
	if _, _, err := ag.getOrCreateSession(context.Background(), "conversation-1"); err != nil {
		t.Fatalf("create session error: %v", err)
	}
	ag.SetClaudeModel("opus", "high")
	if _, isNew, err := ag.getOrCreateSession(context.Background(), "conversation-1"); err != nil || isNew {
		t.Fatalf("existing session=(isNew=%v, err=%v)", isNew, err)
	}
	if calls != 1 {
		t.Fatalf("rpc calls=%d，已有 session 不应重新配置", calls)
	}
}

func TestClaudeACPConfigFailureDoesNotStoreSession(t *testing.T) {
	ag := NewACPAgent(ACPAgentConfig{
		Command: "claude-agent-acp", Model: "opus", StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "session/new" {
			return json.RawMessage(`{"sessionId":"session-1","configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
		}
		return nil, fmt.Errorf("method not supported")
	}

	_, _, err := ag.getOrCreateSession(context.Background(), "conversation-1")

	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("err=%v，期望明确返回 model 配置失败", err)
	}
	ag.mu.Lock()
	stored := ag.sessions["conversation-1"]
	ag.mu.Unlock()
	if stored != "" {
		t.Fatalf("stored=%q，配置失败的 session 不应写入映射", stored)
	}
}

func configOptionValue(params interface{}) string {
	value, ok := params.(sessionConfigOptionParams)
	if !ok {
		return ""
	}
	return value.Value
}
