package agent

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleCodexErrorIgnoresEmptyPayload(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 1)
	a.turnCh["thread-1"] = turnCh

	a.handleCodexError(json.RawMessage(`{}`))

	select {
	case evt := <-turnCh:
		t.Fatalf("空 error 不应终止 turn，实际收到 %#v", evt)
	default:
	}
}

func TestReadLoopRoutesCodexWarningAsProgress(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 2)
	a.turnCh["thread-1"] = turnCh
	raw := `{"method":"warning","params":{"threadId":"thread-1","message":"Falling back from WebSockets to HTTPS transport. stream disconnected before completion"}}`
	a.scanner = bufio.NewScanner(strings.NewReader(raw + "\n"))

	a.readLoop()

	evt := <-turnCh
	if evt.Kind != "progress" || !strings.Contains(evt.Text, "HTTPS") {
		t.Fatalf("warning event=%#v，期望 HTTPS 非致命进度", evt)
	}
}

func TestFormatCodexTurnErrorIncludesAdditionalDetails(t *testing.T) {
	raw := json.RawMessage(`{"message":"request failed","codexErrorInfo":"TransportError","additionalDetails":"request id: req-123"}`)

	got := formatCodexTurnError(raw)

	if !containsAll(got, "request failed", "TransportError", "req-123") {
		t.Fatalf("formatCodexTurnError=%q，期望保留 additionalDetails", got)
	}
}
