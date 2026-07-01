package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func TestRespondCodexApprovalRequestUsesDecisionResult(t *testing.T) {
	var out bytes.Buffer
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.stdin = nopWriteCloser{Buffer: &out}

	if err := a.respondPermissionRequest(json.RawMessage(`12`), "allow", permissionResponseDecision); err != nil {
		t.Fatalf("respondPermissionRequest error: %v", err)
	}

	var resp struct {
		Result struct {
			Decision string `json:"decision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("response json: %v", err)
	}
	if resp.Result.Decision != "allow" {
		t.Fatalf("decision=%q, want allow; raw=%s", resp.Result.Decision, out.String())
	}
}

func TestRespondLegacyPermissionRequestUsesOutcomeResult(t *testing.T) {
	var out bytes.Buffer
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.stdin = nopWriteCloser{Buffer: &out}

	if err := a.respondPermissionRequest(json.RawMessage(`7`), "allow_once", permissionResponseOutcome); err != nil {
		t.Fatalf("respondPermissionRequest error: %v", err)
	}

	var resp struct {
		Result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("response json: %v", err)
	}
	if resp.Result.Outcome.Outcome != "selected" || resp.Result.Outcome.OptionID != "allow_once" {
		t.Fatalf("outcome=%#v, want selected allow_once; raw=%s", resp.Result.Outcome, out.String())
	}
}

func TestReadLoopDispatchesCodexItemApprovalRequestToTurn(t *testing.T) {
	methods := []string{
		"item/fileChange/requestApproval",
		"item/commandExecution/requestApproval",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
			turnCh := make(chan *codexTurnEvent, 1)
			a.notifyMu.Lock()
			a.turnCh["thread-item-approval"] = turnCh
			a.notifyMu.Unlock()

			raw := fmt.Sprintf(`{"jsonrpc":"2.0","id":11,"method":%q,"params":{"threadId":"thread-item-approval","turnId":"turn-1","itemId":"call-1","toolCall":{"cmd":"apply_patch"},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow"},{"optionId":"deny_once","name":"Deny","kind":"deny"}]}}`, method)
			a.mu.Lock()
			a.scanner = bufio.NewScanner(strings.NewReader(raw + "\n"))
			a.mu.Unlock()

			a.readLoop()

			select {
			case evt := <-turnCh:
				assertApprovalEvent(t, evt)
			default:
				t.Fatal("approval request was not dispatched")
			}
		})
	}
}

type nopWriteCloser struct {
	*bytes.Buffer
}

func (n nopWriteCloser) Close() error {
	return nil
}

func assertApprovalEvent(t *testing.T, evt *codexTurnEvent) {
	t.Helper()
	if evt.Approval == nil {
		t.Fatal("approval event missing")
	}
	if string(evt.Approval.ID) != "11" {
		t.Fatalf("approval id=%s, want 11", evt.Approval.ID)
	}
	if len(evt.Approval.Request.Options) != 2 || evt.Approval.Request.Options[0].ID != "allow_once" {
		t.Fatalf("approval options=%#v", evt.Approval.Request.Options)
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
