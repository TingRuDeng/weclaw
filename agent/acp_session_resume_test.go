package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClaudeACPResumeUpdatesBindingOnlyAfterSuccess(t *testing.T) {
	oldWorkspace := t.TempDir()
	selectedWorkspace := t.TempDir()
	agent := newClaudeACPSessionTestAgent(t, oldWorkspace)
	agent.mu.Lock()
	agent.sessions["conversation-1"] = "session-old"
	agent.conversationCwds["conversation-1"] = oldWorkspace
	agent.mu.Unlock()
	var methods []string
	agent.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "session/list":
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-new","cwd":%q}]}`, selectedWorkspace)), nil
		case "session/resume":
			values := marshalParamsForTest(t, params)
			if values["sessionId"] != "session-new" || values["cwd"] != selectedWorkspace {
				t.Fatalf("session/resume params=%#v", values)
			}
			servers, ok := values["mcpServers"].([]interface{})
			if !ok || len(servers) != 0 {
				t.Fatalf("session/resume mcpServers=%#v, want []", values["mcpServers"])
			}
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	err := agent.UseClaudeSession(context.Background(), "conversation-1", "session-new")

	if err != nil {
		t.Fatalf("UseClaudeSession error: %v", err)
	}
	if len(methods) != 2 || methods[0] != "session/list" || methods[1] != "session/resume" {
		t.Fatalf("rpc methods=%v, want list then resume", methods)
	}
	if sessionID, ok := agent.CurrentClaudeSession("conversation-1"); !ok || sessionID != "session-new" {
		t.Fatalf("CurrentClaudeSession=(%q,%v), want session-new true", sessionID, ok)
	}
	if cwd := agent.cwdForConversation("conversation-1"); cwd != selectedWorkspace {
		t.Fatalf("conversation cwd=%q, want %q", cwd, selectedWorkspace)
	}
	if generation, exists := agent.sessionGenerations["conversation-1"]; !exists || generation != agent.legacyRuntimeGeneration {
		t.Fatalf("session generation=(%d,%v), runtime=%d", generation, exists, agent.legacyRuntimeGeneration)
	}
}

func TestClaudeACPResumeFailureKeepsOldBinding(t *testing.T) {
	oldWorkspace := t.TempDir()
	selectedWorkspace := t.TempDir()
	agent := newClaudeACPSessionTestAgent(t, oldWorkspace)
	agent.mu.Lock()
	agent.sessions["conversation-1"] = "session-old"
	agent.conversationCwds["conversation-1"] = oldWorkspace
	agent.mu.Unlock()
	newCalls := 0
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/list":
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-new","cwd":%q}]}`, selectedWorkspace)), nil
		case "session/resume":
			return nil, errors.New("resume rejected")
		case "session/new":
			newCalls++
			return nil, errors.New("must not create")
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	err := agent.UseClaudeSession(context.Background(), "conversation-1", "session-new")

	if err == nil || !containsAll(err.Error(), "session/resume", "resume rejected") {
		t.Fatalf("UseClaudeSession error=%v, want resume failure", err)
	}
	if newCalls != 0 {
		t.Fatalf("session/new calls=%d, want 0", newCalls)
	}
	if sessionID, ok := agent.CurrentClaudeSession("conversation-1"); !ok || sessionID != "session-old" {
		t.Fatalf("CurrentClaudeSession=(%q,%v), want session-old true", sessionID, ok)
	}
	if cwd := agent.cwdForConversation("conversation-1"); cwd != oldWorkspace {
		t.Fatalf("conversation cwd=%q, want old cwd %q", cwd, oldWorkspace)
	}
}

func TestClaudeACPResumeRejectsNonObjectResult(t *testing.T) {
	tests := []struct {
		name   string
		result string
	}{
		{"null", "null"},
		{"字符串", `"ok"`},
		{"数组", "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldWorkspace := t.TempDir()
			selectedWorkspace := t.TempDir()
			agent := newClaudeACPSessionTestAgent(t, oldWorkspace)
			agent.sessions["conversation-1"] = "session-old"
			agent.conversationCwds["conversation-1"] = oldWorkspace
			agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
				if method == "session/list" {
					payload := fmt.Sprintf(`{"sessions":[{"sessionId":"session-new","cwd":%q}]}`, selectedWorkspace)
					return json.RawMessage(payload), nil
				}
				return json.RawMessage(tc.result), nil
			}

			err := agent.UseClaudeSession(context.Background(), "conversation-1", "session-new")

			if err == nil || !containsAll(err.Error(), "session/resume", "JSON object") {
				t.Fatalf("UseClaudeSession error=%v, want non-object result error", err)
			}
			if sessionID, _ := agent.CurrentClaudeSession("conversation-1"); sessionID != "session-old" {
				t.Fatalf("session=%q, want old binding", sessionID)
			}
			if cwd := agent.cwdForConversation("conversation-1"); cwd != oldWorkspace {
				t.Fatalf("cwd=%q, want old cwd %q", cwd, oldWorkspace)
			}
		})
	}
}

func TestClaudeACPResumeRejectsUnknownSession(t *testing.T) {
	agent := newClaudeACPSessionTestAgent(t, t.TempDir())
	resumeCalls := 0
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "session/resume" {
			resumeCalls++
		}
		return json.RawMessage(`{"sessions":[]}`), nil
	}

	err := agent.UseClaudeSession(context.Background(), "conversation-1", "missing-session")

	if err == nil || !containsAll(err.Error(), "missing-session", "session/list") {
		t.Fatalf("UseClaudeSession error=%v, want missing catalog entry", err)
	}
	if resumeCalls != 0 {
		t.Fatalf("session/resume calls=%d, want 0", resumeCalls)
	}
}

func TestClaudeACPResumeRuntimeBindingIsNotPersistedOrRestored(t *testing.T) {
	workspace := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "mock-acp", Cwd: workspace, StateFile: stateFile,
	})
	agent.mu.Lock()
	agent.sessions["conversation-1"] = "runtime-session"
	agent.mu.Unlock()
	agent.persistState()

	persisted := readACPStateFile(t, stateFile)
	if len(persisted.Sessions) != 0 {
		t.Fatalf("persisted Claude sessions=%#v, want empty", persisted.Sessions)
	}
	writeACPStateFile(t, stateFile, acpPersistedState{
		Version:  acpPersistedStateVersion,
		Sessions: map[string]string{"conversation-1": "stale-session"},
	})
	restored := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "claude", Command: "mock-acp", Cwd: workspace, StateFile: stateFile,
	})
	if sessionID, ok := restored.CurrentClaudeSession("conversation-1"); ok || sessionID != "" {
		t.Fatalf("restored Claude session=(%q,%v), want empty false", sessionID, ok)
	}
}

func TestClaudeACPResumeHandshakeIdentityDoesNotRestorePersistedSession(t *testing.T) {
	workspace := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	writeACPStateFile(t, stateFile, acpPersistedState{
		Version:  acpPersistedStateVersion,
		Sessions: map[string]string{"conversation-1": "stale-session"},
	})
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic", Command: "mock-acp", Cwd: workspace, StateFile: stateFile,
	})
	if err := agent.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("cache capabilities: %v", err)
	}

	if sessionID, ok := agent.CurrentClaudeSession("conversation-1"); ok || sessionID != "" {
		t.Fatalf("handshake Claude restored session=(%q,%v), want empty false", sessionID, ok)
	}
}

func TestLegacyACPPersistedSessionsRestoreTogetherAfterHandshake(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	writeACPStateFile(t, stateFile, acpPersistedState{
		Version: acpPersistedStateVersion,
		Sessions: map[string]string{
			"conversation-a": "session-a",
			"conversation-b": "session-b",
		},
	})
	agent := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic", Command: "mock-acp", Cwd: t.TempDir(), StateFile: stateFile,
	})
	if err := agent.cacheAndValidateACPCapabilities(genericCapabilityPayload()); err != nil {
		t.Fatalf("cache capabilities: %v", err)
	}
	agent.mu.Lock()
	restored := map[string]string{
		"conversation-a": agent.sessions["conversation-a"],
		"conversation-b": agent.sessions["conversation-b"],
	}
	agent.mu.Unlock()
	if !reflect.DeepEqual(restored, map[string]string{
		"conversation-a": "session-a", "conversation-b": "session-b",
	}) {
		t.Fatalf("restored sessions=%#v, want all persisted sessions", restored)
	}
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/new" {
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
		return json.RawMessage(`{"sessionId":"session-a-new"}`), nil
	}
	if _, err := agent.ResetSession(context.Background(), "conversation-a"); err != nil {
		t.Fatalf("ResetSession error: %v", err)
	}
	persisted := readACPStateFile(t, stateFile)
	if persisted.Sessions["conversation-a"] != "session-a-new" || persisted.Sessions["conversation-b"] != "session-b" {
		t.Fatalf("persisted sessions=%#v, want updated A and retained B", persisted.Sessions)
	}
}

func genericCapabilityPayload() json.RawMessage {
	return json.RawMessage(`{"protocolVersion":1,"agentInfo":{"name":"generic-agent"},"agentCapabilities":{}}`)
}

func claudeCapabilityPayload() json.RawMessage {
	return json.RawMessage(`{"protocolVersion":1,"agentInfo":{"name":"claude-agent-acp"},"agentCapabilities":{"sessionCapabilities":{"list":{},"resume":{}}}}`)
}

func TestClaudeACPResumeClearOnlyRemovesRuntimeBinding(t *testing.T) {
	workspace := t.TempDir()
	agent := newClaudeACPSessionTestAgent(t, workspace)
	agent.mu.Lock()
	agent.sessions["conversation-1"] = "session-1"
	agent.conversationCwds["conversation-1"] = workspace
	agent.sessionGenerations["conversation-1"] = 3
	agent.mu.Unlock()

	agent.ClearClaudeSession("conversation-1")

	if sessionID, ok := agent.CurrentClaudeSession("conversation-1"); ok || sessionID != "" {
		t.Fatalf("CurrentClaudeSession=(%q,%v), want empty false", sessionID, ok)
	}
	if cwd := agent.cwdForConversation("conversation-1"); cwd != workspace {
		t.Fatalf("ClearClaudeSession changed cwd=%q, want %q", cwd, workspace)
	}
	if _, exists := agent.sessionGenerations["conversation-1"]; exists {
		t.Fatal("ClearClaudeSession did not remove session generation")
	}
}
