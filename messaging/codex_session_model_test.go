package messaging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSessionModelStatusFindsOnlyMatchingRollout(t *testing.T) {
	codexDir := t.TempDir()
	threadID := "00000000-0000-4000-8000-000000000001"
	sessionDir := filepath.Join(codexDir, "sessions", "2026", "07", "26")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sessionDir, "rollout-2026-07-26T00-00-00-"+threadID+".jsonl")
	transcript := `{"type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/workspace"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-old","effort":"medium"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-current","effort":"high"}}` + "\n"
	if err := os.WriteFile(target, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sessionDir, "unrelated.jsonl"),
		[]byte(`{"type":"session_meta","payload":{"id":"`+threadID+`","cwd":"/wrong"}}`+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(codexDir)
	status := h.codexSessionModelStatus(threadID)
	if status.Model != "gpt-current" || status.Effort != "high" {
		t.Fatalf("status=%#v", status)
	}
}

func TestFindLocalCodexSessionPathRejectsUnsafeOrMismatchedCandidates(t *testing.T) {
	root := t.TempDir()
	threadID := "00000000-0000-4000-8000-000000000002"
	candidate := filepath.Join(root, "rollout-"+threadID+".jsonl")
	if err := os.WriteFile(
		candidate,
		[]byte(`{"type":"session_meta","payload":{"id":"different-thread","cwd":"/workspace"}}`+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if path, ok := findLocalCodexSessionPath(root, threadID); ok || path != "" {
		t.Fatalf("mismatched metadata accepted: path=%q", path)
	}
	if path, ok := findLocalCodexSessionPath(root, "../"+threadID); ok || path != "" {
		t.Fatalf("unsafe thread id accepted: path=%q", path)
	}
}
