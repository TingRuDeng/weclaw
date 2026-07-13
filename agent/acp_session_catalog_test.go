package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeACPListTraversesOpaquePagination(t *testing.T) {
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	agent := newClaudeACPSessionTestAgent(t, workspace)
	var cursors []interface{}
	agent.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method != "session/list" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		values := marshalParamsForTest(t, params)
		cursors = append(cursors, values["cursor"])
		if values["cursor"] == nil {
			if !reflect.DeepEqual(values, map[string]interface{}{}) {
				t.Fatalf("first session/list params=%#v, want empty object without cwd", values)
			}
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-1","cwd":%q,"title":"首个","updatedAt":"2026-07-13T01:00:00Z"}],"nextCursor":"opaque/+== cursor"}`, workspace)), nil
		}
		if !reflect.DeepEqual(values, map[string]interface{}{"cursor": "opaque/+== cursor"}) {
			t.Fatalf("next session/list params=%#v, want cursor only", values)
		}
		if values["cursor"] != "opaque/+== cursor" {
			return nil, fmt.Errorf("cursor=%v, want opaque value", values["cursor"])
		}
		return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-2","cwd":%q}]}`, otherWorkspace)), nil
	}

	sessions, err := agent.ListClaudeSessions(context.Background())

	if err != nil {
		t.Fatalf("ListClaudeSessions error: %v", err)
	}
	want := []ClaudeSession{
		{ID: "session-1", Cwd: workspace, Title: "首个", UpdatedAt: "2026-07-13T01:00:00Z"},
		{ID: "session-2", Cwd: otherWorkspace},
	}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("sessions=%#v, want %#v", sessions, want)
	}
	if !reflect.DeepEqual(cursors, []interface{}{nil, "opaque/+== cursor"}) {
		t.Fatalf("cursors=%#v, want opaque cursor unchanged", cursors)
	}
}

func TestClaudeACPListRejectsInvalidNextCursor(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"显式 null", "null"},
		{"空字符串", `""`},
		{"数字", "1"},
		{"数组", "[]"},
		{"对象", "{}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := newClaudeACPSessionTestAgent(t, t.TempDir())
			agent.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
				return json.RawMessage(`{"sessions":[],"nextCursor":` + tc.value + `}`), nil
			}

			_, err := agent.ListClaudeSessions(context.Background())

			if err == nil || !containsAll(err.Error(), "session/list", "nextCursor") {
				t.Fatalf("ListClaudeSessions error=%v, want invalid nextCursor error", err)
			}
		})
	}
}

func TestClaudeACPListRejectsDuplicateSessionAcrossPages(t *testing.T) {
	workspace := t.TempDir()
	agent := newClaudeACPSessionTestAgent(t, workspace)
	calls := 0
	agent.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"duplicate","cwd":%q}],"nextCursor":"page-2"}`, workspace)), nil
		}
		return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"duplicate","cwd":%q}]}`, workspace)), nil
	}

	_, err := agent.ListClaudeSessions(context.Background())

	if err == nil || !containsAll(err.Error(), "session/list", "重复 sessionId", "duplicate") {
		t.Fatalf("ListClaudeSessions error=%v, want duplicate session error", err)
	}
}

func TestACPCapabilityFailureKeepsStateAndAllowsRetry(t *testing.T) {
	agent := newClaudeACPSessionTestAgent(t, t.TempDir())
	agent.configuredName = "generic"
	original := acpCapabilitySnapshot{ProtocolVersion: 1, AgentInfo: acpInitializeAgentInfo{Name: "original"}}
	agent.capabilities = original
	agent.pendingPersistedSessions["conversation-a"] = "session-a"
	agent.pendingPersistedSessions["conversation-b"] = "session-b"
	agent.sessions["existing"] = "session-existing"
	agent.legacyRuntimeGeneration = 7

	err := agent.cacheAndValidateACPCapabilities(json.RawMessage(
		`{"protocolVersion":1,"agentInfo":{"name":"claude-agent-acp"},"agentCapabilities":{"sessionCapabilities":{"list":{}}}}`,
	))

	if err == nil || !strings.Contains(err.Error(), testACPResumeCapability) {
		t.Fatalf("cache capabilities error=%v, want missing resume", err)
	}
	assertACPHandshakeState(t, agent, original, 7, 2, 1)
	if err := agent.cacheAndValidateACPCapabilities(genericCapabilityPayload()); err != nil {
		t.Fatalf("generic retry error: %v", err)
	}
	if agent.capabilities.AgentInfo.Name != "generic-agent" || agent.legacyRuntimeGeneration != 8 {
		t.Fatalf("retry capabilities=%+v generation=%d", agent.capabilities, agent.legacyRuntimeGeneration)
	}
	if len(agent.pendingPersistedSessions) != 0 || len(agent.sessions) != 3 {
		t.Fatalf("retry pending=%#v sessions=%#v", agent.pendingPersistedSessions, agent.sessions)
	}
}

func assertACPHandshakeState(t *testing.T, agent *ACPAgent, capabilities acpCapabilitySnapshot, generation uint64, pending int, sessions int) {
	t.Helper()
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if !reflect.DeepEqual(agent.capabilities, capabilities) || agent.legacyRuntimeGeneration != generation {
		t.Fatalf("capabilities=%+v generation=%d changed after failure", agent.capabilities, agent.legacyRuntimeGeneration)
	}
	if len(agent.pendingPersistedSessions) != pending || len(agent.sessions) != sessions {
		t.Fatalf("pending=%#v sessions=%#v changed after failure", agent.pendingPersistedSessions, agent.sessions)
	}
}

func TestClaudeACPListRejectsRepeatedCursor(t *testing.T) {
	agent := newClaudeACPSessionTestAgent(t, t.TempDir())
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/list" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		return json.RawMessage(`{"sessions":[],"nextCursor":"same-cursor"}`), nil
	}

	_, err := agent.ListClaudeSessions(context.Background())

	if err == nil || !containsAll(err.Error(), "session/list", "重复游标", "same-cursor") {
		t.Fatalf("ListClaudeSessions error=%v, want repeated cursor error", err)
	}
}

func TestClaudeACPListUsesConfiguredOrHandshakeIdentityOnly(t *testing.T) {
	workspace := t.TempDir()
	commandOnly := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic", Command: "claude-agent-acp", Cwd: workspace,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	commandOnly.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
		return nil, fmt.Errorf("rpc must not be called")
	}
	if _, err := commandOnly.ListClaudeSessions(context.Background()); err == nil || !strings.Contains(err.Error(), "不是 Claude ACP") {
		t.Fatalf("command-only identity error=%v, want explicit rejection", err)
	}

	handshake := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic", Command: "mock-acp", Cwd: workspace,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	handshake.capabilities.AgentInfo.Name = claudeACPScopedAgentName
	handshake.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/list" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		return json.RawMessage(`{"sessions":[]}`), nil
	}
	if _, err := handshake.ListClaudeSessions(context.Background()); err != nil {
		t.Fatalf("handshake identity ListClaudeSessions error: %v", err)
	}
}

func TestClaudeACPListRejectsInvalidSessions(t *testing.T) {
	workspace := t.TempDir()
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"空 sessionId", fmt.Sprintf(`{"sessions":[{"sessionId":" ","cwd":%q}]}`, workspace), "sessionId"},
		{"带空白 sessionId", fmt.Sprintf(`{"sessions":[{"sessionId":" session-1 ","cwd":%q}]}`, workspace), "首尾空白"},
		{"空 cwd", `{"sessions":[{"sessionId":"session-1","cwd":""}]}`, "cwd"},
		{"相对 cwd", `{"sessions":[{"sessionId":"session-1","cwd":"relative/workspace"}]}`, "绝对"},
		{"未清理 cwd", fmt.Sprintf(`{"sessions":[{"sessionId":"session-1","cwd":%q}]}`, workspace+string(filepath.Separator)+".."+string(filepath.Separator)+filepath.Base(workspace)), "干净"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := newClaudeACPSessionTestAgent(t, workspace)
			agent.rpcCall = func(_ context.Context, _ string, _ interface{}) (json.RawMessage, error) {
				return json.RawMessage(tc.payload), nil
			}

			_, err := agent.ListClaudeSessions(context.Background())

			if err == nil || !containsAll(err.Error(), "session/list", tc.want) {
				t.Fatalf("ListClaudeSessions error=%v, want %q", err, tc.want)
			}
		})
	}
}

func newClaudeACPSessionTestAgent(t *testing.T, workspace string) *ACPAgent {
	t.Helper()
	return NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude",
		Command:        "mock-acp",
		Cwd:            workspace,
		StateFile:      filepath.Join(t.TempDir(), "state.json"),
	})
}

func marshalParamsForTest(t *testing.T, params interface{}) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	return values
}

func assertNoWhitespace(t *testing.T, value string) {
	t.Helper()
	if strings.TrimSpace(value) != value {
		t.Fatalf("value=%q contains surrounding whitespace", value)
	}
}
