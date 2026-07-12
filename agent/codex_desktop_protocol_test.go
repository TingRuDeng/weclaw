package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexDesktopEnvelopeMatchesGoldenInitialize(t *testing.T) {
	want := readCodexDesktopFixture(t, "initialize_request.json")
	envelope, err := decodeCodexDesktopEnvelope(want)
	if err != nil {
		t.Fatalf("decodeCodexDesktopEnvelope() error = %v", err)
	}
	got, err := encodeCodexDesktopEnvelope(envelope)
	if err != nil {
		t.Fatalf("encodeCodexDesktopEnvelope() error = %v", err)
	}
	if !bytes.Equal(compactCodexDesktopJSON(t, got), compactCodexDesktopJSON(t, want)) {
		t.Fatalf("encoded initialize = %s, want %s", got, want)
	}
}

func TestCodexDesktopEnvelopeRejectsUnknownBroadcastVersion(t *testing.T) {
	payload := []byte(`{"type":"broadcast","version":10,"method":"thread-stream-state-changed","params":{}}`)

	_, err := decodeCodexDesktopEnvelope(payload)
	if !errors.Is(err, ErrCodexDesktopIncompatible) {
		t.Fatalf("decodeCodexDesktopEnvelope() error = %v, want incompatible", err)
	}
}

func TestCodexDesktopEnvelopeRejectsInvalidJSON(t *testing.T) {
	_, err := decodeCodexDesktopEnvelope([]byte(`{"type":"request"`))
	if err == nil {
		t.Fatal("decodeCodexDesktopEnvelope() error = nil, want invalid JSON error")
	}
}

func TestCodexDesktopEnvelopeRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"request missing params", `{"type":"request","requestId":"request-1","version":1,"method":"initialize"}`, "params"},
		{"request null params", `{"type":"request","requestId":"request-1","version":1,"method":"initialize","params":null}`, "params"},
		{"request non-object params", `{"type":"request","requestId":"request-1","version":1,"method":"initialize","params":[]}`, "params"},
		{"broadcast missing params", `{"type":"broadcast","version":11,"method":"thread-stream-state-changed"}`, "params"},
		{"broadcast null params", `{"type":"broadcast","version":11,"method":"thread-stream-state-changed","params":null}`, "params"},
		{"response missing resultType", `{"type":"response","requestId":"response-1"}`, "resultType"},
		{"success response missing result", `{"type":"response","requestId":"response-1","resultType":"success"}`, "result"},
		{"error response missing error", `{"type":"response","requestId":"response-1","resultType":"error"}`, "error"},
		{"response invalid resultType", `{"type":"response","requestId":"response-1","resultType":"pending"}`, "resultType"},
		{"discovery response null", `{"type":"client-discovery-response","requestId":"discover-1","response":null}`, "response"},
		{"discovery response not object", `{"type":"client-discovery-response","requestId":"discover-1","response":[]}`, "response"},
		{"discovery response missing canHandle", `{"type":"client-discovery-response","requestId":"discover-1","response":{}}`, "canHandle"},
		{"discovery response invalid canHandle", `{"type":"client-discovery-response","requestId":"discover-1","response":{"canHandle":"yes"}}`, "canHandle"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeCodexDesktopEnvelope([]byte(test.payload))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeCodexDesktopEnvelope() error = %v, want field %s error", err, test.want)
			}
		})
	}
}

func TestCodexDesktopRequestConstructorRejectsNilParams(t *testing.T) {
	_, err := newCodexDesktopRequest(codexDesktopRequestSpec{
		RequestID:      "request-1",
		SourceClientID: "weclaw-client",
		Method:         "initialize",
		Params:         nil,
	})
	if err == nil || !strings.Contains(err.Error(), "params") {
		t.Fatalf("newCodexDesktopRequest() error = %v, want params error", err)
	}
}

func TestCodexDesktopEnvelopeAcceptsExplicitNullSuccessResult(t *testing.T) {
	payload := []byte(`{"type":"response","requestId":"response-1","resultType":"success","result":null}`)
	if _, err := decodeCodexDesktopEnvelope(payload); err != nil {
		t.Fatalf("decodeCodexDesktopEnvelope() error = %v", err)
	}
}

func TestCodexDesktopDiscoveryResponseAcceptsBooleanCanHandle(t *testing.T) {
	payload := []byte(`{"type":"client-discovery-response","requestId":"discover-1","response":{"canHandle":false}}`)
	if _, err := decodeCodexDesktopEnvelope(payload); err != nil {
		t.Fatalf("decodeCodexDesktopEnvelope() error = %v", err)
	}
}

func TestCodexDesktopDiscoveryAcceptsUnsupportedNestedRequest(t *testing.T) {
	payload := []byte(`{"type":"client-discovery-request","requestId":"discover-1","request":{"type":"request","requestId":"nested-1","sourceClientId":"desktop-client","version":0,"method":"ide-context","params":{"workspaceRoot":"/path/to/project"}}}`)

	envelope, err := decodeCodexDesktopEnvelope(payload)
	if err != nil {
		t.Fatalf("decodeCodexDesktopEnvelope() error = %v", err)
	}
	if envelope.Type != codexDesktopEnvelopeDiscoveryRequest {
		t.Fatalf("envelope.Type = %q", envelope.Type)
	}
}

func TestCodexDesktopDiscoveryCarriesNestedMethodVersion(t *testing.T) {
	envelope, err := newCodexDesktopDiscoveryRequest(codexDesktopDiscoverySpec{
		RequestID:      "discover-1",
		SourceClientID: "weclaw-client",
		Method:         "thread-follower-interrupt-turn",
		Params: map[string]string{
			"conversationId": "thread-1",
			"turnId":         "turn-1",
		},
	})
	if err != nil {
		t.Fatalf("newCodexDesktopDiscoveryRequest() error = %v", err)
	}

	nested, err := decodeCodexDesktopEnvelope(envelope.Request)
	if err != nil {
		t.Fatalf("nested request decode error = %v", err)
	}
	if nested.Method != "thread-follower-interrupt-turn" || nested.Version != 2 {
		t.Fatalf("nested method/version = %s@%d, want thread-follower-interrupt-turn@2", nested.Method, nested.Version)
	}
}

func TestCodexDesktopEnvelopeAcceptsGoldenThreadChanges(t *testing.T) {
	for _, name := range []string{"thread_snapshot.json", "thread_patches.json"} {
		t.Run(name, func(t *testing.T) {
			envelope, err := decodeCodexDesktopEnvelope(readCodexDesktopFixture(t, name))
			if err != nil {
				t.Fatalf("decodeCodexDesktopEnvelope() error = %v", err)
			}
			if envelope.Method != "thread-stream-state-changed" || envelope.Version != 11 {
				t.Fatalf("method/version = %s@%d", envelope.Method, envelope.Version)
			}
		})
	}
}

func readCodexDesktopFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex_desktop", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func compactCodexDesktopJSON(t *testing.T, payload []byte) []byte {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		t.Fatalf("compact JSON: %v", err)
	}
	return compact.Bytes()
}
