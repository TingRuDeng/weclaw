package cmd

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodeHTTPClientHasHeaderTimeoutWithoutWholeRequestDeadline(t *testing.T) {
	client := newOpenCodeHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport=%T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout <= 0 || transport.TLSHandshakeTimeout <= 0 || transport.DialContext == nil {
		t.Fatalf("transport=%+v, want bounded connection and response headers", transport)
	}
	if client.Timeout != 0 {
		t.Fatalf("client timeout=%s, SSE body must be controlled by context", client.Timeout)
	}
}

func TestHandleOpenCodeEventLineBuildsFinalTextOnIdle(t *testing.T) {
	resultCh := make(chan openCodeTurnResult, 1)
	var builder strings.Builder
	var finalText string
	var progress []string

	done := handleOpenCodeEventLine(`{"type":"session.next.text.delta","properties":{"sessionID":"ses_1","delta":"hel"}}`, "ses_1", &builder, &finalText, func(text string) {
		progress = append(progress, text)
	}, resultCh)
	if done {
		t.Fatal("delta event should not finish turn")
	}
	_ = handleOpenCodeEventLine(`{"type":"session.next.text.ended","properties":{"sessionID":"ses_1","text":"hello"}}`, "ses_1", &builder, &finalText, nil, resultCh)
	done = handleOpenCodeEventLine(`{"type":"session.idle","properties":{"sessionID":"ses_1"}}`, "ses_1", &builder, &finalText, nil, resultCh)
	if !done {
		t.Fatal("idle event should finish turn")
	}
	result := <-resultCh
	if result.err != nil || result.text != "hello" {
		t.Fatalf("result = %#v, want final text", result)
	}
	if len(progress) != 1 || progress[0] != "hel" {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestHandleOpenCodeEventLineIgnoresOtherSession(t *testing.T) {
	resultCh := make(chan openCodeTurnResult, 1)
	var builder strings.Builder
	var finalText string

	done := handleOpenCodeEventLine(`{"type":"session.idle","properties":{"sessionID":"ses_other"}}`, "ses_1", &builder, &finalText, nil, resultCh)
	if done {
		t.Fatal("other session event should be ignored")
	}
	select {
	case result := <-resultCh:
		t.Fatalf("unexpected result: %#v", result)
	default:
	}
}

func TestReadEventStreamReturnsErrorOnEOFBeforeIdle(t *testing.T) {
	runtime := &openCodeCompanionRuntime{}
	resultCh := make(chan openCodeTurnResult, 1)

	runtime.readEventStream(t.Context(), bytes.NewReader(nil), "ses_1", nil, resultCh)

	result := <-resultCh
	if result.err == nil || !strings.Contains(result.err.Error(), "ended before session idle") {
		t.Fatalf("result = %#v, want EOF before idle error", result)
	}
}
