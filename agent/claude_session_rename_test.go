package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestACPAgentRenamesClaudeSessionThroughAdvertisedCommandAndReadsBack(t *testing.T) {
	workspace := t.TempDir()
	a := newClaudeRenameTestAgent(t, workspace)
	var methods []string
	listCalls := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "session/list":
			listCalls++
			title := "旧名称"
			if listCalls == 2 {
				title = "新 名称"
			}
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-rename","cwd":%q,"title":%q}]}`, workspace, title)), nil
		case "session/resume":
			values := marshalParamsForTest(t, params)
			if values["sessionId"] != "session-rename" || values["cwd"] != workspace {
				t.Fatalf("session/resume params=%#v", values)
			}
			a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"rename","description":"Rename session"}]}}`))
			return claudeConfigResultForTest("sonnet", "medium"), nil
		case "session/prompt":
			values := marshalParamsForTest(t, params)
			want := map[string]interface{}{
				"sessionId": "session-rename",
				"prompt":    []interface{}{map[string]interface{}{"type": "text", "text": "/rename 新 名称"}},
			}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("session/prompt params=%#v, want %#v", values, want)
			}
			a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"session_info_update","title":"新 名称"}}`))
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	if err := a.RenameClaudeSession(context.Background(), " session-rename ", "新 名称"); err != nil {
		t.Fatalf("RenameClaudeSession error: %v", err)
	}
	wantMethods := []string{"session/list", "session/resume", "session/prompt", "session/list"}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("rpc methods=%v, want %v", methods, wantMethods)
	}
}

func TestACPAgentClaudeRenameFailsClosedWithoutAdvertisedCommand(t *testing.T) {
	workspace := t.TempDir()
	a := newClaudeRenameTestAgent(t, workspace)
	promptCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/list":
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-rename","cwd":%q,"title":"旧名称"}]}`, workspace)), nil
		case "session/resume":
			a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"compact","description":"Compact"}]}}`))
			return claudeConfigResultForTest("sonnet", "medium"), nil
		case "session/prompt":
			promptCalls++
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	err := a.RenameClaudeSession(context.Background(), "session-rename", "新名称")
	if !errors.Is(err, ErrClaudeRenameUnsupported) || promptCalls != 0 {
		t.Fatalf("rename error=%v promptCalls=%d", err, promptCalls)
	}
}

func TestACPAgentClaudeRenameCapabilityTimeoutDoesNotSendPrompt(t *testing.T) {
	workspace := t.TempDir()
	a := newClaudeRenameTestAgent(t, workspace)
	promptCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/list":
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-rename","cwd":%q}]}`, workspace)), nil
		case "session/resume":
			return claudeConfigResultForTest("sonnet", "medium"), nil
		case "session/prompt":
			promptCalls++
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := a.RenameClaudeSession(ctx, "session-rename", "新名称")
	if !errors.Is(err, ErrClaudeRenameUnsupported) || promptCalls != 0 {
		t.Fatalf("rename error=%v promptCalls=%d", err, promptCalls)
	}
}

func TestACPAgentClaudeRenameReadbackMismatchIsUnknownOutcome(t *testing.T) {
	workspace := t.TempDir()
	a := newClaudeRenameTestAgent(t, workspace)
	listCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/list":
			listCalls++
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-rename","cwd":%q,"title":"旧名称"}]}`, workspace)), nil
		case "session/resume":
			a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"rename"}]}}`))
			return claudeConfigResultForTest("sonnet", "medium"), nil
		case "session/prompt":
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	err := a.RenameClaudeSession(context.Background(), "session-rename", "新名称")
	if !errors.Is(err, ErrClaudeRenameOutcomeUnknown) || listCalls != 2 {
		t.Fatalf("rename error=%v listCalls=%d", err, listCalls)
	}
}

func TestACPAgentClaudeRenameRejectsActivePromptBeforeMutation(t *testing.T) {
	workspace := t.TempDir()
	a := newClaudeRenameTestAgent(t, workspace)
	a.mu.Lock()
	a.claudeLoadedSessions["session-rename"] = claudeLoadedSessionState{Cwd: workspace, Generation: a.legacyRuntimeGeneration}
	a.claudeSessionCommands["session-rename"] = claudeSessionCommandState{
		Generation: a.legacyRuntimeGeneration, Known: true, Names: map[string]struct{}{"rename": {}},
	}
	a.mu.Unlock()
	notify := make(chan *sessionUpdate, 1)
	approval := make(chan *codexTurnEvent, 1)
	if !a.registerLegacySessionChannels("session-rename", notify, approval) {
		t.Fatal("failed to seed active prompt")
	}
	defer a.unregisterLegacySessionChannels("session-rename", notify, approval)
	promptCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "session/list" {
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-rename","cwd":%q}]}`, workspace)), nil
		}
		promptCalls++
		return json.RawMessage(`{}`), nil
	}

	err := a.RenameClaudeSession(context.Background(), "session-rename", "新名称")
	if !errors.Is(err, ErrClaudeSessionWriterBusy) || promptCalls != 0 {
		t.Fatalf("rename error=%v promptCalls=%d", err, promptCalls)
	}
}

func TestACPAgentClaudeAvailableCommandsUpdateReplacesAndInvalidatesSnapshot(t *testing.T) {
	a := newClaudeRenameTestAgent(t, t.TempDir())
	a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"rename"}]}}`))
	known, supported, invalid, _ := a.claudeRenameCommandSnapshot("session-rename")
	if !known || !supported || invalid != "" {
		t.Fatalf("initial snapshot=(known=%t supported=%t invalid=%q)", known, supported, invalid)
	}

	a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"compact"}]}}`))
	known, supported, invalid, _ = a.claudeRenameCommandSnapshot("session-rename")
	if !known || supported || invalid != "" {
		t.Fatalf("replacement snapshot=(known=%t supported=%t invalid=%q)", known, supported, invalid)
	}

	a.handleSessionUpdate(json.RawMessage(`{"sessionId":"session-rename","update":{"sessionUpdate":"available_commands_update"}}`))
	known, supported, invalid, _ = a.claudeRenameCommandSnapshot("session-rename")
	if !known || supported || invalid == "" {
		t.Fatalf("invalid snapshot=(known=%t supported=%t invalid=%q)", known, supported, invalid)
	}
}

func TestACPAgentClaudeHandshakeClearsHostRenameState(t *testing.T) {
	workspace := t.TempDir()
	a := newClaudeRenameTestAgent(t, workspace)
	a.mu.Lock()
	a.claudeLoadedSessions["session-rename"] = claudeLoadedSessionState{Cwd: workspace, Generation: a.legacyRuntimeGeneration}
	a.claudeSessionCommands["session-rename"] = claudeSessionCommandState{
		Generation: a.legacyRuntimeGeneration, Known: true, Names: map[string]struct{}{"rename": {}},
	}
	a.claudeSessionTitles["session-rename"] = claudeSessionTitleState{
		Generation: a.legacyRuntimeGeneration, Known: true, Title: "旧名称",
	}
	previousGeneration := a.legacyRuntimeGeneration
	changed := a.claudeCommandChanged
	a.mu.Unlock()

	if err := a.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("second handshake: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.legacyRuntimeGeneration != previousGeneration+1 {
		t.Fatalf("generation=%d, want %d", a.legacyRuntimeGeneration, previousGeneration+1)
	}
	if len(a.claudeLoadedSessions) != 0 || len(a.claudeSessionCommands) != 0 || len(a.claudeSessionTitles) != 0 {
		t.Fatalf("host state not cleared: loaded=%#v commands=%#v titles=%#v", a.claudeLoadedSessions, a.claudeSessionCommands, a.claudeSessionTitles)
	}
	select {
	case <-changed:
	default:
		t.Fatal("handshake did not wake capability waiters")
	}
}

func newClaudeRenameTestAgent(t *testing.T, workspace string) *ACPAgent {
	t.Helper()
	a := newClaudeACPSessionTestAgent(t, workspace)
	if err := a.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("cache capabilities: %v", err)
	}
	return a
}
