package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
