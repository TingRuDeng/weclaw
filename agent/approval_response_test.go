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

func TestResolvePermissionOptionUsesProtocolDeclineWhenOptionsMissing(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	called := false
	ctx := ContextWithApprovalHandler(context.Background(), func(context.Context, ApprovalRequest) (string, error) {
		called = true
		return "", fmt.Errorf("approval request has no options")
	})

	got := a.resolvePermissionOption(ctx, ApprovalRequest{})

	if got != "decline" {
		t.Fatalf("fallback option=%q, want decline", got)
	}
	if called {
		t.Fatal("approval handler was called for request without options")
	}
}

func TestSelectPermissionOptionUsesCodexFileChangeDecisionFallback(t *testing.T) {
	params := permissionRequestParams{
		AvailableDecisionsSnake: permissionDecisions{"accept", "cancel"},
	}

	got := selectPermissionOption(params, "deny")

	if got != "cancel" {
		t.Fatalf("fallback decision=%q, want cancel", got)
	}
}

func TestSelectPermissionOptionDoesNotInventInvalidDenyDecision(t *testing.T) {
	params := permissionRequestParams{}

	got := selectPermissionOption(params, "deny")

	if got != "decline" {
		t.Fatalf("fallback decision=%q, want decline", got)
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
	var threadSandbox string
	var turnSandboxType string

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			p, ok := params.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected thread/start params type %T", params)
			}
			threadSandbox, _ = p["sandbox"].(string)
			return json.RawMessage(`{"thread":{"id":"thread-approval"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			turnApprovalPolicy = p.ApprovalPolicy
			sandbox, _ := p.SandboxPolicy.(map[string]interface{})
			turnSandboxType, _ = sandbox["type"].(string)
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
	if threadSandbox != "workspace-write" || turnSandboxType != "workspaceWrite" {
		t.Fatalf("default sandbox thread=%q turn=%q, want workspace-write/workspaceWrite", threadSandbox, turnSandboxType)
	}
}

func TestCodexAppServerUsesConfiguredApprovalAndSandbox(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command:        "codex",
		Args:           []string{"app-server", "--listen", "stdio://"},
		Cwd:            t.TempDir(),
		ApprovalPolicy: "on-request",
		SandboxMode:    "workspace-write",
	})
	var threadApprovalPolicy string
	var threadApprovalReviewer string
	var threadSandbox string
	var turnApprovalPolicy string
	var turnApprovalReviewer string
	var turnSandboxType string

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			p, ok := params.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected thread/start params type %T", params)
			}
			threadApprovalPolicy, _ = p["approvalPolicy"].(string)
			threadApprovalReviewer, _ = p["approvalsReviewer"].(string)
			threadSandbox, _ = p["sandbox"].(string)
			return json.RawMessage(`{"thread":{"id":"thread-configured"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			turnApprovalPolicy = p.ApprovalPolicy
			turnApprovalReviewer = p.ApprovalsReviewer
			sandbox, _ := p.SandboxPolicy.(map[string]interface{})
			turnSandboxType, _ = sandbox["type"].(string)
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

	if _, err := a.chatCodexAppServer(context.Background(), "conversation-configured", "hello", nil); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if threadApprovalPolicy != "on-request" || turnApprovalPolicy != "on-request" {
		t.Fatalf("approval policies thread=%q turn=%q, want on-request", threadApprovalPolicy, turnApprovalPolicy)
	}
	if threadApprovalReviewer != "" || turnApprovalReviewer != "" {
		t.Fatalf("approval reviewers thread=%q turn=%q, want empty", threadApprovalReviewer, turnApprovalReviewer)
	}
	if threadSandbox != "workspace-write" || turnSandboxType != "workspaceWrite" {
		t.Fatalf("sandbox thread=%q turn=%q, want workspace-write/workspaceWrite", threadSandbox, turnSandboxType)
	}
}

func TestCodexAppServerUsesConfiguredApprovalReviewer(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command:          "codex",
		Args:             []string{"app-server", "--listen", "stdio://"},
		Cwd:              t.TempDir(),
		ApprovalPolicy:   "on-request",
		ApprovalReviewer: "auto_review",
		SandboxMode:      "workspace-write",
	})
	var threadApprovalReviewer string
	var turnApprovalReviewer string

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			p, ok := params.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected thread/start params type %T", params)
			}
			threadApprovalReviewer, _ = p["approvalsReviewer"].(string)
			return json.RawMessage(`{"thread":{"id":"thread-reviewer"}}`), nil
		case "turn/start":
			p, ok := params.(codexTurnStartParams)
			if !ok {
				return nil, fmt.Errorf("unexpected turn/start params type %T", params)
			}
			turnApprovalReviewer = p.ApprovalsReviewer
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

	if _, err := a.chatCodexAppServer(context.Background(), "conversation-reviewer", "hello", nil); err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if threadApprovalReviewer != "auto_review" || turnApprovalReviewer != "auto_review" {
		t.Fatalf("approval reviewers thread=%q turn=%q, want auto_review", threadApprovalReviewer, turnApprovalReviewer)
	}
}
