package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
