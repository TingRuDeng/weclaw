package agent

import (
	"context"
	"encoding/json"
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
		if evt.Approval.Request.RequestID != "7" {
			t.Fatalf("approval request id=%q, want 7", evt.Approval.Request.RequestID)
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

func TestHandleStandardACPPermissionRequestDispatchesBySessionID(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "claude-agent-acp"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["session-1"] = turnCh
	a.turnCh["session-2"] = make(chan *codexTurnEvent, 1)
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":8,"method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"title":"Remove file"},"options":[{"optionId":"allow-once","name":"Allow once","kind":"allow_once"},{"optionId":"reject-once","name":"Reject once","kind":"reject_once"}]}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		options := evt.Approval.Request.Options
		if len(options) != 2 || options[0].Kind != "allow" || options[1].Kind != "deny" {
			t.Fatalf("approval options=%#v, want normalized allow/deny kinds", options)
		}
	default:
		t.Fatal("standard ACP permission request was not dispatched by sessionId")
	}
}

func TestHandleCodexCommandApprovalUsesAvailableDecisions(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-approval"] = turnCh
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":12,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-approval","turnId":"turn-1","itemId":"call-1","approvalId":3,"command":["date"],"cwd":"/tmp","availableDecisions":["allow","deny"]}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		if len(evt.Approval.Request.Options) != 2 || evt.Approval.Request.Options[0].ID != "allow" {
			t.Fatalf("approval options=%#v, want available decisions", evt.Approval.Request.Options)
		}
		var tool map[string]string
		if err := json.Unmarshal(evt.Approval.Request.ToolCall, &tool); err != nil {
			t.Fatalf("tool call json: %v", err)
		}
		if tool["cmd"] != "date" || tool["cwd"] != "/tmp" {
			t.Fatalf("tool call=%#v, want command and cwd", tool)
		}
	default:
		t.Fatal("approval request was not dispatched")
	}
}

func TestHandleCodexCommandApprovalUsesSnakeCaseAvailableDecisions(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-approval"] = turnCh
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":12,"method":"item/fileChange/requestApproval","params":{"threadId":"thread-approval","turnId":"turn-1","itemId":"call-1","approvalId":3,"available_decisions":["accept","cancel"],"changes":"apply_patch touching agent/acp_agent.go"}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		options := evt.Approval.Request.Options
		if len(options) != 2 || options[0].ID != "accept" || options[1].Kind != "deny" {
			t.Fatalf("approval options=%#v, want snake_case available decisions", options)
		}
	default:
		t.Fatal("approval request was not dispatched")
	}
}

func TestHandleCodexCommandApprovalAcceptsStringCommand(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-approval"] = turnCh
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":12,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-approval","turnId":"turn-1","itemId":"call-1","approvalId":3,"command":"date","cwd":"/tmp","availableDecisions":["allow","deny"]}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		var tool map[string]string
		if err := json.Unmarshal(evt.Approval.Request.ToolCall, &tool); err != nil {
			t.Fatalf("tool call json: %v", err)
		}
		if tool["cmd"] != "date" || tool["cwd"] != "/tmp" {
			t.Fatalf("tool call=%#v, want string command and cwd", tool)
		}
	default:
		t.Fatal("approval request was not dispatched")
	}
}

func TestPermissionCommandAcceptsObjectCommand(t *testing.T) {
	var command permissionCommand
	if err := json.Unmarshal([]byte(`{"cmd":"go test ./agent"}`), &command); err != nil {
		t.Fatalf("unmarshal object command: %v", err)
	}
	if got := string(command[0]); got != "go test ./agent" {
		t.Fatalf("command=%q, want object cmd", got)
	}
}

func TestHandleCodexCommandApprovalAcceptsObjectAvailableDecisions(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-approval"] = turnCh
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":12,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-approval","turnId":"turn-1","itemId":"call-1","approvalId":3,"command":"date","cwd":"/tmp","availableDecisions":[{"decision":"allow","label":"Allow"},{"decision":"deny","label":"Deny"}]}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		options := evt.Approval.Request.Options
		if len(options) != 2 || options[0].ID != "allow" || options[1].ID != "deny" {
			t.Fatalf("approval options=%#v, want object available decisions", options)
		}
	default:
		t.Fatal("approval request was not dispatched")
	}
}

func TestHandleCodexCommandApprovalMapsAcceptCancelDecisions(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	turnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-approval"] = turnCh
	a.notifyMu.Unlock()

	raw := `{"jsonrpc":"2.0","id":12,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-approval","turnId":"turn-1","itemId":"call-1","approvalId":3,"command":"date","cwd":"/tmp","availableDecisions":["accept","cancel"]}}`
	a.handlePermissionRequest(raw)

	select {
	case evt := <-turnCh:
		if evt.Approval == nil {
			t.Fatal("approval event missing")
		}
		options := evt.Approval.Request.Options
		if len(options) != 2 || options[0].Kind != "allow" || options[1].Kind != "deny" {
			t.Fatalf("approval options=%#v, want accept/cancel mapped to allow/deny", options)
		}
	default:
		t.Fatal("approval request was not dispatched")
	}
}
