package messaging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeSessionStoreMigratesV2SingleBindingToRemoteOwner(t *testing.T) {
	workspace := t.TempDir()
	key := claudeBindingKey("route-a", "claude")
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	state := map[string]any{
		"version": 2,
		"bindings": map[string]any{
			key: map[string]any{
				"workspace_root": workspace,
				"session_id":     "session-a",
				"status":         "ready",
				"updated_at":     "2026-07-15T00:00:00Z",
			},
		},
		"updated": "2026-07-15T00:00:00Z",
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	intent := store.controlIntent("session-a")
	wantConversation := buildClaudeConversationID("route-a", "claude", workspace)
	if intent.Owner != claudeOwnerRemote || intent.BindingKey != key ||
		intent.ConversationID != wantConversation || intent.Revision != 1 {
		t.Fatalf("intent=%+v", intent)
	}
}

func TestClaudeSessionStoreMigratesV2ConflictingBindingsToUnclaimed(t *testing.T) {
	workspace := t.TempDir()
	bindings := map[string]claudeSessionBinding{
		claudeBindingKey("route-a", "claude"): newClaudeBinding(workspace, "session-shared", claudeBindingReady),
		claudeBindingKey("route-b", "claude"): newClaudeBinding(workspace, "session-shared", claudeBindingReady),
	}
	data, err := json.Marshal(claudeSessionState{Version: 2, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	decodedBindings, controls, migrated, err := decodeClaudeSessionState(data)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || len(decodedBindings) != 2 {
		t.Fatalf("migrated=%v bindings=%+v", migrated, decodedBindings)
	}
	intent := controls["session-shared"]
	if intent.Owner != claudeOwnerUnclaimed || intent.BindingKey != "" || intent.ConversationID != "" {
		t.Fatalf("intent=%+v", intent)
	}
}

func TestClaudeSessionStoreReloadPreservesLocalOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.controls["session-local"] = claudeControlIntent{Owner: claudeOwnerLocal, Revision: 4}
	state := newClaudeSessionState(store.bindings, store.controls)
	store.mu.Unlock()
	if err := persistClaudeSessionState(path, state); err != nil {
		t.Fatal(err)
	}
	reloaded := newClaudeSessionStore()
	if err := reloaded.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.controlIntent("session-local"); got.Owner != claudeOwnerLocal || got.Revision != 4 {
		t.Fatalf("intent=%+v", got)
	}
}

func TestClaudeSessionStoreLoadsReadyBindingAsPendingResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	first := newClaudeSessionStore()
	if err := first.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	if err := first.commitSelection("route", "/tmp/project", "session-1"); err != nil {
		t.Fatal(err)
	}

	restored := newClaudeSessionStore()
	if err := restored.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	binding := restored.binding("route")
	if binding.SessionID != "session-1" || binding.Status != claudeBindingPendingResume {
		t.Fatalf("binding=%+v, want pending resume", binding)
	}
}

func TestClaudeSessionStoreMigratesV1ActiveWorkspaceOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	legacy := `{"version":1,"bindings":{"route":{"ActiveWorkspace":"/tmp/project","Workspaces":{"/tmp/project":{"ThreadID":"legacy-cli-session"}}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	binding := store.binding("wechat:route")
	if binding.WorkspaceRoot != "/tmp/project" || binding.SessionID != "" || binding.Status != claudeBindingUnbound {
		t.Fatalf("binding=%+v, want workspace-only migration", binding)
	}
}

func TestClaudeSessionStoreRollsBackMemoryWhenSaveFails(t *testing.T) {
	store := newClaudeSessionStore()
	if err := store.commitSelection("route", "/tmp/old", "session-old"); err != nil {
		t.Fatal(err)
	}
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }

	err := store.commitSelection("route", "/tmp/new", "session-new")
	if err == nil {
		t.Fatal("commitSelection error=nil, want save failure")
	}
	binding := store.binding("route")
	if binding.WorkspaceRoot != "/tmp/old" || binding.SessionID != "session-old" {
		t.Fatalf("binding=%+v, want old binding after rollback", binding)
	}
}

func TestClaudeSessionStoreRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"bindings":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err == nil {
		t.Fatal("SetFilePath error=nil，期望拒绝未知状态版本")
	}
	if len(store.bindings) != 0 {
		t.Fatalf("bindings=%#v，解析失败不应发布状态", store.bindings)
	}
}

func TestClaudeSessionStoreMigrationPrefersCanonicalKey(t *testing.T) {
	legacy := `{"version":1,"bindings":{"route":{"ActiveWorkspace":"/tmp/legacy"},"wechat:route":{"ActiveWorkspace":"/tmp/canonical"}}}`
	bindings, _, migrated, err := decodeClaudeSessionState([]byte(legacy))
	if err != nil || !migrated {
		t.Fatalf("migrated=%t err=%v", migrated, err)
	}
	binding := bindings["wechat:route"]
	if binding.WorkspaceRoot != "/tmp/canonical" {
		t.Fatalf("binding=%+v，规范键必须覆盖旧键迁移结果", binding)
	}
}
