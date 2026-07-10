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

	var progress []string
	reply, err := a.chatLegacyACP(ctx, "user-1", "hello", func(delta string) {
		progress = append(progress, delta)
	}, false)
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
			}}
			notifyCh <- &sessionUpdate{SessionUpdate: "agent_message_chunk", Content: json.RawMessage(`{"type":"text","text":"done"}`)}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	reply, err := a.chatLegacyACP(ctx, "conversation-1", "hello", nil, false)
	if err != nil {
		t.Fatalf("chatLegacyACP error: %v", err)
	}
	if reply != "done" || !strings.Contains(out.String(), `"optionId":"reject-once"`) {
		t.Fatalf("reply=%q response=%s", reply, out.String())
	}
}
