package agent

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/observability"
)

type protocolTraceCapture struct {
	mu      sync.Mutex
	records []observability.ProtocolRecord
}

func (capture *protocolTraceCapture) RecordProtocol(record observability.ProtocolRecord) error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	record.Raw = append([]byte(nil), record.Raw...)
	capture.records = append(capture.records, record)
	return nil
}

type protocolTraceBuffer struct{ bytes.Buffer }

func (*protocolTraceBuffer) Close() error { return nil }

func TestACPProtocolTraceRecordsOutboundContextAndInboundSequence(t *testing.T) {
	capture := &protocolTraceCapture{}
	ag := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"},
		ProtocolTrace: capture,
	})
	ag.stdin = &protocolTraceBuffer{}
	ag.wireEpoch = 4
	trace := observability.NewTraceContext(observability.TraceSeed{MessageID: "message-1"}).WithAgent("codex")
	ctx := observability.ContextWithTrace(context.Background(), trace)
	contextTrace, ok := observability.TraceFromContext(ctx)
	if !ok {
		t.Fatal("trace missing from context")
	}
	if err := ag.writeJSONLineWithTrace([]byte(`{"jsonrpc":"2.0","id":1,"method":"turn/start"}`), contextTrace); err != nil {
		t.Fatal(err)
	}
	ag.handleACPWireLine(`{"jsonrpc":"2.0","method":"thread/status/changed","params":{"threadId":"thread-1"}}`)

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.records) != 2 {
		t.Fatalf("records=%#v", capture.records)
	}
	if capture.records[0].Direction != "outbound" || capture.records[0].Trace.TraceID != trace.TraceID || capture.records[0].WireEpoch != 4 {
		t.Fatalf("outbound=%#v", capture.records[0])
	}
	if capture.records[1].Direction != "inbound" || capture.records[1].Sequence != 1 || capture.records[1].WireEpoch != 4 {
		t.Fatalf("inbound=%#v", capture.records[1])
	}
}
