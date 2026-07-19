package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSessionStateV3DropsOwnersAndKeepsAllBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	legacy := `{
	  "version": 3,
	  "bindings": {
	    "wechat:route-a\u0000claude": {"workspace_root":"/workspace","session_id":"session-shared","status":"ready","updated_at":"2026-07-18T10:00:00Z"},
	    "wechat:route-b\u0000claude": {"workspace_root":"/workspace","session_id":"session-shared","status":"ready","updated_at":"2026-07-18T10:01:00Z"}
  },
  "controls": {
    "session-shared": {"owner":"unclaimed","revision":9,"updated_at":"2026-07-18T10:02:00Z"}
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"route-a", "route-b"} {
		binding := store.binding(claudeBindingKey(route, "claude"))
		if binding.SessionID != "session-shared" || binding.Status != claudeBindingPendingResume || binding.Revision == 0 {
			t.Fatalf("route=%s binding=%+v", route, binding)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"controls"`) {
		t.Fatalf("migrated state still persists owner map: %s", data)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Version != claudeSessionStateVersion {
		t.Fatalf("header=%+v err=%v", header, err)
	}
}

func TestClaudeSessionStateRoundTripRestartsAsPendingResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	first := newClaudeSessionStore()
	if err := first.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	key := claudeBindingKey("route-a", "claude")
	if err := first.commitSelection(key, "/workspace", "session-a"); err != nil {
		t.Fatal(err)
	}
	written := first.binding(key)
	if written.Status != claudeBindingReady || written.Revision == 0 {
		t.Fatalf("written=%+v", written)
	}

	restored := newClaudeSessionStore()
	if err := restored.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	got := restored.binding(key)
	if got.SessionID != "session-a" || got.Status != claudeBindingPendingResume || got.Revision <= written.Revision {
		t.Fatalf("restored=%+v written=%+v", got, written)
	}
}

func TestClaudeSessionStateCorruptionIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	want := []byte("{broken")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err == nil {
		t.Fatal("SetFilePath error=nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("corrupt file overwritten: %q", got)
	}
}

func TestDecodeClaudeSessionStateRejectsUnknownVersion(t *testing.T) {
	if _, _, err := decodeClaudeSessionState([]byte(`{"version":99,"bindings":{}}`)); err == nil {
		t.Fatal("decode error=nil")
	}
}

func TestClaudeSessionStateV4PreservesTwoFrontendBindings(t *testing.T) {
	state := newClaudeSessionState(map[string]claudeSessionBinding{
		claudeBindingKey("route-a", "claude"): newClaudeBinding("/workspace", "session-shared", claudeBindingReady),
		claudeBindingKey("route-b", "claude"): newClaudeBinding("/workspace", "session-shared", claudeBindingReady),
	})
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	bindings, migrated, err := decodeClaudeSessionState(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || !migrated {
		t.Fatalf("bindings=%+v migrated=%v", bindings, migrated)
	}
}
