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
		ConfiguredName: "claude", Command: "claude-agent-acp", Model: "opus", Effort: "high",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	var calls []string
	ag.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls = append(calls, method+":"+configOptionValue(params))
		switch method {
		case "session/new":
			return json.RawMessage(`{"sessionId":"session-1","configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
		case "session/set_config_option":
			return claudeConfigResultForTest("opus", "low", "medium", "high"), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	sessionID, err := ag.createSession(context.Background(), "conversation-1")

	if err != nil || sessionID != "session-1" {
		t.Fatalf("session=(%q,%v)，期望新 session-1", sessionID, err)
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

func TestClaudeACPMapsCanonicalModelIDToSessionConfigValue(t *testing.T) {
	const initialOptions = `[
		{"id":"model","currentValue":"sonnet","options":[
			{"value":"sonnet","name":"Sonnet"},
			{"value":"fable","name":"Fable"}
		]}
	]`
	const configuredOptions = `[
		{"id":"model","currentValue":"fable","options":[
			{"value":"sonnet","name":"Sonnet"},
			{"value":"fable","name":"Fable"}
		]}
	]`
	ag := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", Model: "claude-fable-5",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	var configured []string
	ag.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "session/new":
			return json.RawMessage(`{"sessionId":"session-1","configOptions":` + initialOptions + `}`), nil
		case "session/set_config_option":
			configured = append(configured, configOptionValue(params))
			return json.RawMessage(`{"configOptions":` + configuredOptions + `}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	sessionID, err := ag.createSession(context.Background(), "conversation-1")

	if err != nil || sessionID != "session-1" {
		t.Fatalf("session=(%q,%v)，期望规范模型 ID 能创建新 session", sessionID, err)
	}
	if strings.Join(configured, ",") != "fable" {
		t.Fatalf("configured=%#v，期望向 ACP 写入 session 实际选项 fable", configured)
	}
}

func TestClaudeACPDoesNotReconfigureExistingSession(t *testing.T) {
	ag := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	calls := 0
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		calls++
		if method != "session/new" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return json.RawMessage(`{"sessionId":"session-1","configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
	}
	if _, err := ag.createSession(context.Background(), "conversation-1"); err != nil {
		t.Fatalf("create session error: %v", err)
	}
	ag.SetClaudeModel("opus", "high")
	if _, err := ag.requireSession("conversation-1"); err != nil {
		t.Fatalf("existing session error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("rpc calls=%d，已有 session 不应重新配置", calls)
	}
}

func TestClaudeACPConfigFailureDoesNotStoreSession(t *testing.T) {
	ag := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", Model: "opus", StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "session/new" {
			return json.RawMessage(`{"sessionId":"session-1","configOptions":` + claudeACPConfigOptionsJSON + `}`), nil
		}
		return nil, fmt.Errorf("method not supported")
	}

	_, err := ag.createSession(context.Background(), "conversation-1")

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

func TestClaudeACPCachesEffortOptionsPerModel(t *testing.T) {
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	agent.cacheClaudeConfigOptions(claudeConfigOptionsForTest(t, "sonnet", "low", "medium"))
	agent.cacheClaudeConfigOptions(claudeConfigOptionsForTest(t, "opus", "high", "max"))

	models, err := agent.ListClaudeModels(context.Background())
	if err != nil {
		t.Fatalf("ListClaudeModels error: %v", err)
	}
	sonnet := claudeModelByAliasForTest(t, models, "sonnet")
	opus := claudeModelByAliasForTest(t, models, "opus")
	if strings.Join(sonnet.EffortOptions, ",") != "low,medium" {
		t.Fatalf("sonnet efforts=%#v, want low,medium", sonnet.EffortOptions)
	}
	if strings.Join(opus.EffortOptions, ",") != "high,max" {
		t.Fatalf("opus efforts=%#v, want high,max", opus.EffortOptions)
	}
}

func TestClaudeACPModelChangeClearsPreviousEffort(t *testing.T) {
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", Model: "sonnet", Effort: "high",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})

	agent.SetClaudeModel("opus", "")

	status := agent.ClaudeModelStatus()
	if status.Model != "opus" || status.Effort != "" {
		t.Fatalf("status=%#v, want opus with default effort", status)
	}
}

func TestClaudeACPDoesNotAdvertiseUnobservedEffortOptions(t *testing.T) {
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", StateFile: filepath.Join(t.TempDir(), "state.json"),
	})

	models, err := agent.ListClaudeModels(context.Background())
	if err != nil {
		t.Fatalf("ListClaudeModels error: %v", err)
	}
	for _, model := range models {
		if len(model.EffortOptions) != 0 {
			t.Fatalf("model %s efforts=%#v, want unknown", model.Alias, model.EffortOptions)
		}
	}
}

func TestClaudeACPRejectsEffortUnsupportedBySelectedModel(t *testing.T) {
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "claude-agent-acp", Model: "opus", Effort: "low",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	var configured []string
	agent.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "session/new":
			return claudeSessionResultForTest("sonnet", "low", "medium"), nil
		case "session/set_config_option":
			value := configOptionValue(params)
			configured = append(configured, value)
			if value == "opus" {
				return claudeConfigResultForTest("opus", "high", "max"), nil
			}
			return nil, fmt.Errorf("unexpected effort configuration %q", value)
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	_, err := agent.createSession(context.Background(), "conversation-1")

	if err == nil || !strings.Contains(err.Error(), "不支持推理强度") {
		t.Fatalf("err=%v, want unsupported effort error", err)
	}
	if strings.Join(configured, ",") != "opus" {
		t.Fatalf("configured=%#v, invalid effort must not reach ACP", configured)
	}
}

func claudeConfigOptionsForTest(t *testing.T, currentModel string, efforts ...string) []acpSessionConfigOption {
	t.Helper()
	options := []acpSessionConfigOption{{
		ID: claudeModelConfigID, CurrentValue: currentModel,
		Options: []acpSessionConfigChoice{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
	}, {
		ID: claudeEffortConfigID, CurrentValue: firstClaudeEffortForTest(efforts),
	}}
	for _, effort := range efforts {
		options[1].Options = append(options[1].Options, acpSessionConfigChoice{Value: effort, Name: effort})
	}
	return options
}

func claudeSessionResultForTest(currentModel string, efforts ...string) json.RawMessage {
	return claudeConfigEnvelopeForTest("session-1", currentModel, efforts)
}

func claudeConfigResultForTest(currentModel string, efforts ...string) json.RawMessage {
	return claudeConfigEnvelopeForTest("", currentModel, efforts)
}

func claudeConfigEnvelopeForTest(sessionID string, currentModel string, efforts []string) json.RawMessage {
	options := []acpSessionConfigOption{{
		ID: claudeModelConfigID, CurrentValue: currentModel,
		Options: []acpSessionConfigChoice{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
	}, {
		ID: claudeEffortConfigID, CurrentValue: firstClaudeEffortForTest(efforts),
	}}
	for _, effort := range efforts {
		options[1].Options = append(options[1].Options, acpSessionConfigChoice{Value: effort, Name: effort})
	}
	payload := map[string]interface{}{"configOptions": options}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	data, _ := json.Marshal(payload)
	return data
}

func firstClaudeEffortForTest(efforts []string) string {
	if len(efforts) == 0 {
		return ""
	}
	return efforts[0]
}

func claudeModelByAliasForTest(t *testing.T, models []ClaudeModel, alias string) ClaudeModel {
	t.Helper()
	for _, model := range models {
		if model.Alias == alias {
			return model
		}
	}
	t.Fatalf("model alias %q not found", alias)
	return ClaudeModel{}
}

func configOptionValue(params interface{}) string {
	value, ok := params.(sessionConfigOptionParams)
	if !ok {
		return ""
	}
	return value.Value
}
