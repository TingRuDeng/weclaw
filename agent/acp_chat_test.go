package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
		Command:   "claude-agent-acp",
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	sessionStarts := 0
	rpcCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		rpcCalls++
		if method == "session/new" {
			sessionStarts++
		}
		return nil, fmt.Errorf("unexpected rpc method: %s", method)
	}

	_, err := a.Chat(context.Background(), "conversation-1", "hello")
	if err == nil {
		t.Fatal("Chat error = nil, want session not bound")
	}
	if sessionStarts != 0 {
		t.Fatalf("session/new calls=%d, want 0", sessionStarts)
	}
	if rpcCalls != 0 {
		t.Fatalf("rpc calls=%d, want 0", rpcCalls)
	}
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
