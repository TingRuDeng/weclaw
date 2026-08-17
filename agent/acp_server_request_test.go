package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestACPAgentRoutesToolUserInputRequestAndRespondsWithAnswers(t *testing.T) {
	a, output := newACPServerRequestTestAgent(t)
	turnCh := make(chan *codexTurnEvent, 1)
	observerID := a.registerTurnObserver("thread-1", turnCh)
	defer a.unregisterTurnObserver("thread-1", observerID, turnCh)

	a.handleACPWireLine(`{"jsonrpc":"2.0","id":62,"method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","questions":[{"id":"mode","header":"执行方式","question":"请选择执行方式","options":[{"label":"快速","description":"优先速度"},{"label":"完整","description":"完整检查"}]}]}}`)

	var event *codexTurnEvent
	select {
	case event = <-turnCh:
	case <-time.After(time.Second):
		t.Fatalf("user input request was not routed; response=%s", output.String())
	}
	if event.UserInput == nil || event.TurnID != "turn-1" || event.ItemID != "item-1" {
		t.Fatalf("event=%#v", event)
	}
	request := event.UserInput.Request
	if request.RequestID != "62" || len(request.Questions) != 1 || request.Questions[0].Prompt != "请选择执行方式" {
		t.Fatalf("request=%#v", request)
	}
	ctx := ContextWithUserInputHandler(context.Background(), func(context.Context, UserInputRequest) (UserInputAnswers, error) {
		return UserInputAnswers{"mode": {"快速"}}, nil
	})
	if err := a.handleCodexUserInputEvent(ctx, event); err != nil {
		t.Fatalf("handleCodexUserInputEvent() error=%v", err)
	}

	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Result  struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}
	decodeACPServerResponse(t, output, &response)
	if response.JSONRPC != "2.0" || response.ID != 62 || response.Error != nil {
		t.Fatalf("response=%+v", response)
	}
	if got := response.Result.Answers["mode"].Answers; len(got) != 1 || got[0] != "快速" {
		t.Fatalf("answers=%#v", response.Result.Answers)
	}
	if pending := a.claimPendingCodexInteractions("thread-1"); len(pending) != 0 {
		t.Fatalf("pending interactions=%#v, want none after provider response", pending)
	}
	if brokers := a.turnInteractionBrokers["thread-1"]; len(brokers) != 0 {
		t.Fatalf("interaction brokers=%#v, want none after provider response", brokers)
	}
}

func TestACPAgentServerRequestResolvedSettlesPendingInteraction(t *testing.T) {
	a, _ := newACPServerRequestTestAgent(t)
	turnCh := make(chan *codexTurnEvent, 1)
	observerID := a.registerTurnObserver("thread-1", turnCh)
	defer a.unregisterTurnObserver("thread-1", observerID, turnCh)

	a.handleACPWireLine(`{"jsonrpc":"2.0","id":63,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"printf protocol-check","cwd":"/tmp","availableDecisions":["accept","decline"]}}`)

	var event *codexTurnEvent
	select {
	case event = <-turnCh:
	case <-time.After(time.Second):
		t.Fatal("approval request was not routed")
	}
	if event.Approval == nil || event.Approval.Request.Resolution == nil {
		t.Fatalf("approval event=%#v, want shared interaction resolution", event)
	}

	resolved := rpcResponse{
		Method: "serverRequest/resolved",
		Params: json.RawMessage(`{"threadId":"thread-1","requestId":63}`),
	}
	if !a.dispatchCodexNotification(resolved, "") {
		t.Fatal("serverRequest/resolved notification was not consumed")
	}
	select {
	case <-event.Approval.Request.Resolution.Done():
	case <-time.After(time.Second):
		t.Fatal("external resolution did not settle the pending approval")
	}
	if err := event.Approval.Request.Resolution.Err(); !errors.Is(err, ErrCodexInteractionResolvedExternally) {
		t.Fatalf("resolution error=%v, want ErrCodexInteractionResolvedExternally", err)
	}
	if pending := a.claimPendingCodexInteractions("thread-1"); len(pending) != 0 {
		t.Fatalf("pending interactions=%#v, want none after external resolution", pending)
	}
	if brokers := a.turnInteractionBrokers["thread-1"]; len(brokers) != 0 {
		t.Fatalf("interaction brokers=%#v, want none after external resolution", brokers)
	}
}

func TestACPAgentTerminalEventSettlesAndForgetsTurnInteractions(t *testing.T) {
	a, _ := newACPServerRequestTestAgent(t)
	turnCh := make(chan *codexTurnEvent, 2)
	observerID := a.registerTurnObserver("thread-1", turnCh)
	defer a.unregisterTurnObserver("thread-1", observerID, turnCh)

	request := &codexApprovalRequest{Request: ApprovalRequest{RequestID: "64"}}
	a.dispatchToTurnCh("thread-1", &codexTurnEvent{
		Kind: "approval_request", TurnID: "turn-1", Approval: request,
	})
	event := <-turnCh
	if event.Approval == nil || event.Approval.Request.Resolution == nil {
		t.Fatalf("approval event=%#v, want shared interaction resolution", event)
	}

	a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "completed", TurnID: "turn-1"})
	select {
	case <-event.Approval.Request.Resolution.Done():
	case <-time.After(time.Second):
		t.Fatal("turn terminal event did not settle the pending interaction")
	}
	if err := event.Approval.Request.Resolution.Err(); !errors.Is(err, ErrCodexTurnTerminal) {
		t.Fatalf("resolution error=%v, want ErrCodexTurnTerminal", err)
	}
	if pending := a.claimPendingCodexInteractions("thread-1"); len(pending) != 0 {
		t.Fatalf("pending interactions=%#v, want none after turn terminal", pending)
	}
	if brokers := a.turnInteractionBrokers["thread-1"]; len(brokers) != 0 {
		t.Fatalf("interaction brokers=%#v, want none after turn terminal", brokers)
	}
}

func TestCodexInteractionBrokerPrefersExternalResolutionOverResponderError(t *testing.T) {
	for _, test := range []struct {
		name       string
		resolveErr error
		wantErr    error
	}{
		{name: "resolved by another frontend", resolveErr: ErrCodexInteractionResolvedExternally, wantErr: ErrCodexInteractionResolvedExternally},
		{name: "turn became terminal", resolveErr: ErrCodexTurnTerminal, wantErr: ErrCodexTurnTerminal},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker := newCodexInteractionBroker("thread-1", "turn-1", "approval\x00turn-1\x0063")
			responding := make(chan struct{})
			release := make(chan struct{})
			responderErr := errors.New("stale responder failure")
			done := make(chan error, 1)
			go func() {
				done <- broker.submit(context.Background(), func() error {
					close(responding)
					<-release
					return responderErr
				})
			}()
			select {
			case <-responding:
			case <-time.After(time.Second):
				t.Fatal("responder did not start")
			}

			broker.resolve(test.resolveErr)
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, test.wantErr) || errors.Is(err, responderErr) {
					t.Fatalf("submit error=%v, want authoritative resolution %v", err, test.wantErr)
				}
			case <-time.After(time.Second):
				t.Fatal("submit did not return after responder completed")
			}
		})
	}
}

func TestACPAgentRespondsToUnsupportedDynamicToolCall(t *testing.T) {
	a, output := newACPServerRequestTestAgent(t)

	a.handleACPWireLine(`{"jsonrpc":"2.0","id":60,"method":"item/tool/call","params":{"tool":"list_projects","arguments":{}}}`)

	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Result  struct {
			ContentItems []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"contentItems"`
			Success *bool `json:"success"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}
	decodeACPServerResponse(t, output, &response)
	if response.JSONRPC != "2.0" || response.ID != 60 {
		t.Fatalf("response identity=%+v", response)
	}
	if response.Error != nil {
		t.Fatalf("error=%+v, want dynamic tool failure result", response.Error)
	}
	if response.Result.Success == nil || *response.Result.Success {
		t.Fatalf("success=%v, want explicit false", response.Result.Success)
	}
	if len(response.Result.ContentItems) != 1 ||
		response.Result.ContentItems[0].Type != "inputText" ||
		!strings.Contains(response.Result.ContentItems[0].Text, "WeClaw") {
		t.Fatalf("contentItems=%+v", response.Result.ContentItems)
	}
}

func TestACPAgentRejectsUnknownServerRequest(t *testing.T) {
	a, output := newACPServerRequestTestAgent(t)

	a.handleACPWireLine(`{"jsonrpc":"2.0","id":61,"method":"future/request","params":{}}`)

	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	decodeACPServerResponse(t, output, &response)
	if response.JSONRPC != "2.0" || response.ID != 61 {
		t.Fatalf("response identity=%+v", response)
	}
	if response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("error=%+v, want JSON-RPC method-not-found", response.Error)
	}
	if len(response.Result) != 0 {
		t.Fatalf("result=%s, want no result", response.Result)
	}
}

func newACPServerRequestTestAgent(t *testing.T) (*ACPAgent, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server"},
		Cwd:       root,
		StateFile: filepath.Join(root, "state.json"),
	})
	output := &bytes.Buffer{}
	a.mu.Lock()
	a.stdin = acpServerRequestTestWriteCloser{Buffer: output}
	a.mu.Unlock()
	t.Cleanup(func() {
		a.mu.Lock()
		a.stdin = nil
		a.mu.Unlock()
	})
	return a, output
}

func decodeACPServerResponse(t *testing.T, output *bytes.Buffer, response interface{}) {
	t.Helper()
	data := bytes.TrimSpace(output.Bytes())
	if len(data) == 0 {
		t.Fatal("server request received no JSON-RPC response")
	}
	if err := json.Unmarshal(data, response); err != nil {
		t.Fatalf("response json: %v; raw=%s", err, data)
	}
}

type acpServerRequestTestWriteCloser struct {
	*bytes.Buffer
}

func (acpServerRequestTestWriteCloser) Close() error {
	return nil
}
