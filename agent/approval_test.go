package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestApprovalPolicyForContextUsesUntrustedWithHandler(t *testing.T) {
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return "allow", nil
	})

	if got := approvalPolicyForContext(ctx); got != "untrusted" {
		t.Fatalf("approval policy=%q, want untrusted", got)
	}
	if got := approvalPolicyForContext(context.Background()); got != "never" {
		t.Fatalf("default approval policy=%q, want never", got)
	}
}

func TestHandlePermissionRequestDispatchesApprovalToTurn(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = turnCh
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":7,"method":"turn/approval/request","params":{"threadId":"thread-1","toolCall":{"cmd":"rm file"},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow"},{"optionId":"deny_once","name":"Deny","kind":"deny"}]}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		if string(evt.Approval.ID) != "7" {
			t.Fatalf("approval id=%s, want 7", evt.Approval.ID)
		}
		if len(evt.Approval.Request.Options) != 2 || evt.Approval.Request.Options[1].ID != "deny_once" {
			t.Fatalf("approval options=%#v", evt.Approval.Request.Options)
		}
		var tool map[string]string
		if err := json.Unmarshal(evt.Approval.Request.ToolCall, &tool); err != nil {
			t.Fatalf("tool call json: %v", err)
		}
		if tool["cmd"] != "rm file" {
			t.Fatalf("tool call=%#v", tool)
		}
	default:
		t.Fatal("approval request was not dispatched")
	}
}

func TestCodexTurnStartUsesUntrustedApprovalPolicyWithHandler(t *testing.T) {
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		return "allow", nil
	})
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
		Cwd:     t.TempDir(),
	})
	var turnApprovalPolicy string

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-approval"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			turnApprovalPolicy = p.ApprovalPolicy
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Delta: "ok"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	if _, err := a.chatCodexAppServer(ctx, "conversation-approval", "hello", nil); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if turnApprovalPolicy != "untrusted" {
		t.Fatalf("turn approval policy=%q, want untrusted", turnApprovalPolicy)
	}
}
