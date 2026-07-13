package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestACPAgentChatRequiresCodexThread 验证普通消息不能隐式创建 Codex thread。
func TestACPAgentChatRequiresCodexThread(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	threadStarts := 0
	rpcCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		rpcCalls++
		if method == "thread/start" {
			threadStarts++
		}
		return nil, fmt.Errorf("unexpected rpc method: %s", method)
	}

	_, err := a.Chat(context.Background(), "conversation-1", "hello")
	if err == nil {
		t.Fatal("Chat error = nil, want session not bound")
	}
	if threadStarts != 0 {
		t.Fatalf("thread/start calls=%d, want 0", threadStarts)
	}
	if rpcCalls != 0 {
		t.Fatalf("rpc calls=%d, want 0", rpcCalls)
	}
}

// TestLegacyACPChatRequiresSession 验证普通消息不能隐式创建 Claude session。
func TestLegacyACPChatRequiresSession(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic",
		Command:        filepath.Join(t.TempDir(), "missing-acp"),
		StateFile:      filepath.Join(t.TempDir(), "state.json"),
	})

	_, err := a.Chat(context.Background(), "conversation-1", "hello")
	if !errors.Is(err, ErrAgentSessionNotBound) {
		t.Fatalf("Chat error=%v, want ErrAgentSessionNotBound", err)
	}
	if a.isRuntimeStarted() {
		t.Fatal("Chat started runtime without binding or persisted candidate")
	}
}

// TestLegacyACPChatStartsForPersistedCandidate 验证旧状态存在候选时允许先启动并识别握手身份。
func TestLegacyACPChatStartsForPersistedCandidate(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	writeACPStateFile(t, stateFile, acpPersistedState{
		Version:  acpPersistedStateVersion,
		Sessions: map[string]string{"conversation-1": "persisted-session"},
	})
	command := filepath.Join(t.TempDir(), "missing-acp")
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "generic", Command: command, StateFile: stateFile,
	})

	_, err := a.Chat(context.Background(), "conversation-1", "hello")

	if err == nil || errors.Is(err, ErrAgentSessionNotBound) || !strings.Contains(err.Error(), "start acp agent") {
		t.Fatalf("Chat error=%v, want runtime startup attempt", err)
	}
}

func TestClaudeACPChatLazyResumesAfterRuntimeGenerationChanges(t *testing.T) {
	agent, workspace := prepareClaudeLazyResumeTest(t)
	var methods []string
	agent.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "session/resume":
			assertLazyResumeParams(t, params, workspace)
			return claudeConfigResultForTest("sonnet", "medium"), nil
		case "session/prompt":
			sendLegacyTestReply(t, agent, "session-1", "done")
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := agent.Chat(context.Background(), "conversation-1", "hello")

	if err != nil || reply != "done" {
		t.Fatalf("Chat=(%q,%v), want done nil", reply, err)
	}
	if !reflect.DeepEqual(methods, []string{"session/resume", "session/prompt"}) {
		t.Fatalf("rpc methods=%v, want resume then prompt", methods)
	}
	if agent.sessionGenerations["conversation-1"] != agent.legacyRuntimeGeneration {
		t.Fatalf("session generation=%d runtime=%d", agent.sessionGenerations["conversation-1"], agent.legacyRuntimeGeneration)
	}
}

func TestClaudeACPChatLazyResumeFailureKeepsBinding(t *testing.T) {
	agent, workspace := prepareClaudeLazyResumeTest(t)
	oldGeneration := agent.sessionGenerations["conversation-1"]
	calls := map[string]int{}
	agent.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls[method]++
		if method == "session/resume" {
			assertLazyResumeParams(t, params, workspace)
			return nil, errors.New("resume failed")
		}
		return nil, fmt.Errorf("unexpected rpc method: %s", method)
	}

	_, err := agent.Chat(context.Background(), "conversation-1", "hello")

	if err == nil || !containsAll(err.Error(), "session/resume", "resume failed") {
		t.Fatalf("Chat error=%v, want lazy resume failure", err)
	}
	if calls["session/prompt"] != 0 || calls["session/new"] != 0 {
		t.Fatalf("rpc calls=%#v, must not prompt or create", calls)
	}
	if agent.sessions["conversation-1"] != "session-1" || agent.conversationCwds["conversation-1"] != workspace {
		t.Fatalf("binding=%q cwd=%q changed", agent.sessions["conversation-1"], agent.conversationCwds["conversation-1"])
	}
	if agent.sessionGenerations["conversation-1"] != oldGeneration {
		t.Fatalf("session generation=%d, want %d", agent.sessionGenerations["conversation-1"], oldGeneration)
	}
}

func prepareClaudeLazyResumeTest(t *testing.T) (*ACPAgent, string) {
	t.Helper()
	workspace := t.TempDir()
	agent := newClaudeACPSessionTestAgent(t, workspace)
	if err := agent.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("first handshake: %v", err)
	}
	agent.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method == "session/list" {
			return json.RawMessage(fmt.Sprintf(`{"sessions":[{"sessionId":"session-1","cwd":%q}]}`, workspace)), nil
		}
		return claudeConfigResultForTest("sonnet", "medium"), nil
	}
	if err := agent.UseClaudeSession(context.Background(), "conversation-1", "session-1"); err != nil {
		t.Fatalf("UseClaudeSession: %v", err)
	}
	if err := agent.cacheAndValidateACPCapabilities(claudeCapabilityPayload()); err != nil {
		t.Fatalf("second handshake: %v", err)
	}
	agent.started = true
	return agent, workspace
}

func assertLazyResumeParams(t *testing.T, params interface{}, workspace string) {
	t.Helper()
	values := marshalParamsForTest(t, params)
	if values["sessionId"] != "session-1" || values["cwd"] != workspace {
		t.Fatalf("session/resume params=%#v", values)
	}
	servers, ok := values["mcpServers"].([]interface{})
	if !ok || len(servers) != 0 {
		t.Fatalf("session/resume mcpServers=%#v, want []", values["mcpServers"])
	}
}

func sendLegacyTestReply(t *testing.T, agent *ACPAgent, sessionID string, text string) {
	t.Helper()
	agent.notifyMu.Lock()
	channel := agent.notifyCh[sessionID]
	agent.notifyMu.Unlock()
	if channel == nil {
		t.Fatal("missing notify channel")
	}
	channel <- &sessionUpdate{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(fmt.Sprintf(`{"type":"text","text":%q}`, text))}
}

func TestLegacyACPAgentMessageChunkDoesNotEmitProgress(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	a := NewACPAgent(ACPAgentConfig{Command: "mock", StateFile: stateFile})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "session/new":
			return json.RawMessage(`{"sessionId":"session-1"}`), nil
		case "session/prompt":
			a.notifyMu.Lock()
			ch := a.notifyCh["session-1"]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing notify channel")
			}
			ch <- &sessionUpdate{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"最终回复"}`)}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}
	if _, err := a.createSession(ctx, "user-1"); err != nil {
		t.Fatalf("createSession error: %v", err)
	}

	var progress []string
	reply, err := a.chatLegacyACP(ctx, "user-1", "hello", func(delta string) {
		progress = append(progress, delta)
	})
	if err != nil {
		t.Fatalf("chatLegacyACP error: %v", err)
	}
	if reply != "最终回复" {
		t.Fatalf("reply=%q, want final text", reply)
	}
	if len(progress) != 0 {
		t.Fatalf("progress=%#v, want no ordinary chunks", progress)
	}
}

func TestLegacyACPChatHandlesPermissionRequest(t *testing.T) {
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return "reject-once", nil
	})
	a := NewACPAgent(ACPAgentConfig{Command: "mock", StateFile: filepath.Join(t.TempDir(), "state.json")})
	var out bytes.Buffer
	a.stdin = nopWriteCloser{Buffer: &out}
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/new":
			return json.RawMessage(`{"sessionId":"session-1"}`), nil
		case "session/prompt":
			a.notifyMu.Lock()
			turnCh := a.turnCh["session-1"]
			notifyCh := a.notifyCh["session-1"]
			a.notifyMu.Unlock()
			if turnCh == nil {
				return nil, fmt.Errorf("missing permission channel")
			}
			turnCh <- &codexTurnEvent{Approval: &codexApprovalRequest{
				ID:             json.RawMessage(`7`),
				ResponseFormat: permissionResponseOutcome,
				Request: ApprovalRequest{Options: []ApprovalOption{
					{ID: "allow-once", Kind: "allow"},
					{ID: "reject-once", Kind: "deny"},
				}},
				Respond: func(_ context.Context, optionID string) error {
					return a.respondPermissionRequest(
						json.RawMessage(`7`), optionID, permissionResponseOutcome,
					)
				},
			}}
			notifyCh <- &sessionUpdate{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"done"}`)}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	if _, err := a.createSession(ctx, "conversation-1"); err != nil {
		t.Fatalf("createSession error: %v", err)
	}

	reply, err := a.chatLegacyACP(ctx, "conversation-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatLegacyACP error: %v", err)
	}
	if reply != "done" || !strings.Contains(out.String(), `"optionId":"reject-once"`) {
		t.Fatalf("reply=%q response=%s", reply, out.String())
	}
}
