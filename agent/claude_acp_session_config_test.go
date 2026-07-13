package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeACPSessionConfigUpdatesCurrentSessionOnly(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	ag.sessions["conversation-2"] = "session-2"
	ag.cacheClaudeSessionConfig("session-1", claudeConfigOptionsForTest(t, "sonnet", "low", "medium"))
	ag.cacheClaudeSessionConfig("session-2", claudeConfigOptionsForTest(t, "opus", "high"))
	var calls []sessionConfigOptionParams
	ag.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "session/set_config_option" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		request := params.(sessionConfigOptionParams)
		calls = append(calls, request)
		return claudeConfigResultForTest("opus", "high", "max"), nil
	}
	err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{
		ConversationID: "conversation-1", Model: "opus", Effort: "high",
	})
	if err != nil {
		t.Fatalf("SetClaudeSessionConfig error: %v", err)
	}
	if len(calls) != 2 || calls[0].SessionID != "session-1" || calls[1].SessionID != "session-1" {
		t.Fatalf("calls=%#v, want current session only", calls)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{ag, "conversation-1", ClaudeSessionConfig{"opus", "high"}})
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{ag, "conversation-2", ClaudeSessionConfig{"opus", "high"}})
	defaults := ag.ClaudeModelStatus()
	if defaults.Model != "sonnet" || defaults.Effort != "low" {
		t.Fatalf("defaults=%#v, current session update must not mutate defaults", defaults)
	}
}

func TestClaudeACPSessionConfigRejectsIncompleteResponse(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	ag.cacheClaudeSessionConfig("session-1", claudeConfigOptionsForTest(t, "sonnet", "medium"))
	ag.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{
		ConversationID: "conversation-1", Model: "opus",
	})
	if err == nil || !strings.Contains(err.Error(), "完整 configOptions") {
		t.Fatalf("error=%v, want incomplete response error", err)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{ag, "conversation-1", ClaudeSessionConfig{"sonnet", "medium"}})
}

func TestClaudeACPResumeCachesSessionConfig(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.started = true
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/list":
			return json.RawMessage(`{"sessions":[{"sessionId":"session-1","cwd":"/tmp/project"}]}`), nil
		case "session/resume":
			return claudeConfigResultForTest("opus", "high", "max"), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	if err := ag.UseClaudeSession(context.Background(), "conversation-1", "session-1"); err != nil {
		t.Fatalf("UseClaudeSession error: %v", err)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{ag, "conversation-1", ClaudeSessionConfig{"opus", "high"}})
}

func TestClaudeACPSessionConfigResumeWithoutOptionsKeepsOldBinding(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.started = true
	ag.sessions["conversation-1"] = "old-session"
	ag.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "session/list" {
			return json.RawMessage(`{"sessions":[{"sessionId":"new-session","cwd":"/tmp/project"}]}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	err := ag.UseClaudeSession(context.Background(), "conversation-1", "new-session")
	if err == nil || !strings.Contains(err.Error(), "完整 configOptions") {
		t.Fatalf("UseClaudeSession error=%v", err)
	}
	if sessionID, _ := ag.CurrentClaudeSession("conversation-1"); sessionID != "old-session" {
		t.Fatalf("session=%q, want old binding", sessionID)
	}
}

func TestClaudeACPConfigOptionNotificationReplacesSnapshot(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	ag.cacheClaudeSessionConfig("session-1", claudeConfigOptionsForTest(t, "sonnet", "medium"))
	options := claudeConfigOptionsForTest(t, "opus", "max")
	payload, _ := json.Marshal(sessionUpdateParams{SessionID: "session-1", Update: sessionUpdate{
		SessionUpdate: "config_option_update", ConfigOptions: options,
	}})
	ag.handleSessionUpdate(payload)
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{ag, "conversation-1", ClaudeSessionConfig{"opus", "max"}})
}

func TestClaudeACPSessionConfigRejectsInvalidTargetsAndDuplicateSnapshots(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{ConversationID: "missing", Model: "opus"})
	if err == nil || !strings.Contains(err.Error(), "未绑定") {
		t.Fatalf("unbound error=%v", err)
	}
	ag.sessions["conversation-1"] = "session-1"
	if err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{ConversationID: "conversation-1"}); err == nil {
		t.Fatal("empty update must fail")
	}
	duplicate := claudeConfigOptionsForTest(t, "sonnet", "medium")
	duplicate = append(duplicate, duplicate[0])
	if err := ag.cacheClaudeSessionConfig("session-1", duplicate); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate error=%v", err)
	}
}

func TestClaudeACPSessionConfigAllowsUnavailableRestoredModel(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	options := claudeConfigOptionsForTest(t, "retired-model", "medium")
	if err := ag.cacheClaudeSessionConfig("session-1", options); err != nil {
		t.Fatalf("cache restored model error: %v", err)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{
		ag: ag, conversation: "conversation-1", want: ClaudeSessionConfig{Model: "retired-model", Effort: "medium"},
	})
}

func TestClaudeACPSessionConfigUsesSemanticCategories(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	options := claudeConfigOptionsForTest(t, "sonnet", "medium", "high")
	options[0].ID, options[0].Category = "model-selector", "model"
	options[1].ID, options[1].Category = "thinking-selector", "thought_level"
	if err := ag.cacheClaudeSessionConfig("session-1", options); err != nil {
		t.Fatalf("cache categories error: %v", err)
	}
	var configIDs []string
	ag.rpcCall = func(_ context.Context, _ string, params interface{}) (json.RawMessage, error) {
		request := params.(sessionConfigOptionParams)
		configIDs = append(configIDs, request.ConfigID)
		return claudeConfigResultWithCategoriesForTest(request.ConfigID), nil
	}
	if err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{
		ConversationID: "conversation-1", Model: "opus", Effort: "high",
	}); err != nil {
		t.Fatalf("SetClaudeSessionConfig error: %v", err)
	}
	if strings.Join(configIDs, ",") != "model-selector,thinking-selector" {
		t.Fatalf("config IDs=%#v", configIDs)
	}
}

func TestClaudeACPSessionConfigStopsAfterBindingChanges(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	ag.cacheClaudeSessionConfig("session-1", claudeConfigOptionsForTest(t, "sonnet", "medium", "high"))
	calls := 0
	ag.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		calls++
		ag.ClearClaudeSession("conversation-1")
		ag.mu.Lock()
		ag.sessions["conversation-1"] = "session-2"
		ag.mu.Unlock()
		return claudeConfigResultForTest("opus", "high"), nil
	}
	err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{
		ConversationID: "conversation-1", Model: "opus", Effort: "high",
	})
	if err == nil || !strings.Contains(err.Error(), "绑定已变化") {
		t.Fatalf("error=%v, want binding conflict", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, stale target must not receive effort update", calls)
	}
}

func TestClaudeACPSessionConfigReportsPartialModelUpdate(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	ag.cacheClaudeSessionConfig("session-1", claudeConfigOptionsForTest(t, "sonnet", "medium"))
	calls := 0
	ag.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		calls++
		return claudeConfigResultForTest("opus", "high"), nil
	}
	err := ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{
		ConversationID: "conversation-1", Model: "opus", Effort: "max",
	})
	if err == nil || !strings.Contains(err.Error(), "配置部分完成") {
		t.Fatalf("error=%v, want partial completion", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, unsupported effort must not reach ACP", calls)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{
		ag: ag, conversation: "conversation-1", want: ClaudeSessionConfig{Model: "opus", Effort: "high"},
	})
}

func TestClaudeACPSessionConfigSerializesWrites(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	ag.cacheClaudeSessionConfig("session-1", claudeConfigOptionsForTest(t, "sonnet", "medium"))
	firstEntered, releaseFirst := make(chan struct{}), make(chan struct{})
	var calls []string
	ag.rpcCall = func(_ context.Context, _ string, params interface{}) (json.RawMessage, error) {
		value := params.(sessionConfigOptionParams).Value
		calls = append(calls, value)
		if value == "opus" {
			close(firstEntered)
			<-releaseFirst
		}
		return claudeConfigResultForTest(value, "medium"), nil
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{ConversationID: "conversation-1", Model: "opus"})
	}()
	<-firstEntered
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- ag.SetClaudeSessionConfig(context.Background(), ClaudeSessionConfigUpdate{ConversationID: "conversation-1", Model: "sonnet"})
	}()
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first update error: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second update error: %v", err)
	}
	if strings.Join(calls, ",") != "opus,sonnet" {
		t.Fatalf("calls=%#v, want serialized order", calls)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{
		ag: ag, conversation: "conversation-1", want: ClaudeSessionConfig{Model: "sonnet", Effort: "medium"},
	})
}

func TestClaudeACPSessionConfigKeepsNewestWireSnapshot(t *testing.T) {
	ag := newClaudeConfigAgentForTest(t)
	ag.sessions["conversation-1"] = "session-1"
	oldOptions := claudeConfigOptionsForTest(t, "sonnet", "medium")
	newOptions := claudeConfigOptionsForTest(t, "opus", "high")
	if err := ag.cacheClaudeSessionConfigAt("session-1", newOptions, 12); err != nil {
		t.Fatalf("cache new snapshot error: %v", err)
	}
	if err := ag.cacheClaudeSessionConfigAt("session-1", oldOptions, 11); err != nil {
		t.Fatalf("cache stale snapshot error: %v", err)
	}
	assertClaudeSessionConfig(t, claudeSessionConfigAssertion{
		ag: ag, conversation: "conversation-1", want: ClaudeSessionConfig{Model: "opus", Effort: "high"},
	})
}

func claudeConfigResultWithCategoriesForTest(configID string) json.RawMessage {
	options := []acpSessionConfigOption{
		{ID: "model-selector", Category: "model", CurrentValue: "opus", Options: []acpSessionConfigChoice{{Value: "sonnet"}, {Value: "opus"}}},
		{ID: "thinking-selector", Category: "thought_level", CurrentValue: "high", Options: []acpSessionConfigChoice{{Value: "medium"}, {Value: "high"}}},
	}
	payload, _ := json.Marshal(map[string]interface{}{"configOptions": options, "updated": configID})
	return payload
}

func newClaudeConfigAgentForTest(t *testing.T) *ACPAgent {
	t.Helper()
	ag := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Model: "sonnet", Effort: "low",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	if err := ag.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("capabilities error: %v", err)
	}
	return ag
}

type claudeSessionConfigAssertion struct {
	ag           *ACPAgent
	conversation string
	want         ClaudeSessionConfig
}

func assertClaudeSessionConfig(t *testing.T, assertion claudeSessionConfigAssertion) {
	t.Helper()
	config, ok := assertion.ag.ClaudeSessionConfig(assertion.conversation)
	if !ok || config != assertion.want {
		t.Fatalf("config=(%#v,%v), want %#v", config, ok, assertion.want)
	}
}
