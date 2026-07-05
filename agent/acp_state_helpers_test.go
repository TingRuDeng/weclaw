package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readACPStateFile(t *testing.T, path string) acpPersistedState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file %s: %v", path, err)
	}
	var state acpPersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal state file %s: %v", path, err)
	}
	return state
}

func writeACPStateFile(t *testing.T, path string, state acpPersistedState) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write state file %s: %v", path, err)
	}
}
