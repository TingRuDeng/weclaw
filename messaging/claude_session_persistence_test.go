package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestClaudeSessionStoreFailsClosedNormalizedSessionKeyCollision(t *testing.T) {
	controls := map[string]claudeControlIntent{
		"session-a":   {Owner: claudeOwnerLocal, Revision: 3, UpdatedAt: "2026-07-15T01:00:00Z"},
		" session-a ": {Owner: claudeOwnerLocal, Revision: 9, UpdatedAt: "2026-07-15T02:00:00Z"},
	}
	for i := 0; i < 20; i++ {
		got := normalizeClaudeControls(controls)["session-a"]
		if got.Owner != claudeOwnerUnclaimed || got.Revision != 9 || got.UpdatedAt != "2026-07-15T02:00:00Z" {
			t.Fatalf("iteration=%d intent=%+v, want stable fail-closed collision", i, got)
		}
	}
}

func TestClaudeSessionStoreLogsSanitizedControlDiagnostics(t *testing.T) {
	workspace := t.TempDir()
	key := claudeBindingKey("secret-route", "claude")
	oldOutput := log.Writer()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	controls := map[string]claudeControlIntent{
		"   ":              {Owner: claudeOwnerLocal, Revision: 1},
		"collision":        {Owner: claudeOwnerLocal, Revision: 2},
		" collision ":      {Owner: claudeOwnerLocal, Revision: 3},
		"invalid-control":  {Owner: claudeControlOwner("invalid"), Revision: 4},
		"secret-session-a": {Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "wrong", Revision: 5},
	}
	normalizeClaudeControlsForBindings(map[string]claudeSessionBinding{
		key: newClaudeBinding(workspace, "secret-session-a", claudeBindingReady),
	}, controls)

	got := logs.String()
	for _, reason := range []string{"blank_session_key", "session_key_collision", "invalid_control", "binding_mismatch"} {
		if !strings.Contains(got, "reason="+reason) {
			t.Fatalf("logs=%q, missing reason=%s", got, reason)
		}
	}
	for _, secret := range []string{"secret-route", "secret-session-a", workspace} {
		if strings.Contains(got, secret) {
			t.Fatalf("diagnostic leaked sensitive value %q: %q", secret, got)
		}
	}
}

func TestClaudeSessionStoreFailsClosedContradictoryV3Controls(t *testing.T) {
	workspace := t.TempDir()
	key := claudeBindingKey("route-a", "claude")
	binding := newClaudeBinding(workspace, "session-a", claudeBindingReady)
	wantConversation := buildClaudeConversationID("route-a", "claude", workspace)

	tests := []struct {
		name     string
		controls map[string]claudeControlIntent
		wantIDs  []string
	}{
		{
			name: "remote binding 未选中该 session",
			controls: map[string]claudeControlIntent{
				"session-other": {
					Owner: claudeOwnerRemote, BindingKey: key, ConversationID: wantConversation, Revision: 2,
				},
			},
			wantIDs: []string{"session-other"},
		},
		{
			name: "conversation 与 route workspace 不匹配",
			controls: map[string]claudeControlIntent{
				"session-a": {
					Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "claude\x00wrong", Revision: 3,
				},
			},
			wantIDs: []string{"session-a"},
		},
		{
			name: "同一 binding 声明多个 remote session",
			controls: map[string]claudeControlIntent{
				"session-a": {
					Owner: claudeOwnerRemote, BindingKey: key, ConversationID: wantConversation, Revision: 4,
				},
				"session-other": {
					Owner: claudeOwnerRemote, BindingKey: key, ConversationID: wantConversation, Revision: 5,
				},
			},
			wantIDs: []string{"session-a", "session-other"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(claudeSessionState{
				Version: claudeSessionStateVersion,
				Bindings: map[string]claudeSessionBinding{
					key: binding,
				},
				Controls: tt.controls,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "claude-sessions.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			store := newClaudeSessionStore()
			if err := store.SetFilePath(path); err != nil {
				t.Fatal(err)
			}
			for _, sessionID := range tt.wantIDs {
				intent := store.controlIntent(sessionID)
				if intent.Owner != claudeOwnerUnclaimed || intent.BindingKey != "" || intent.ConversationID != "" {
					t.Fatalf("session=%s intent=%+v, want fail-closed unclaimed", sessionID, intent)
				}
			}
		})
	}
}

func TestClaudeSessionStorePreservesConsistentV3RemoteControl(t *testing.T) {
	workspace := t.TempDir()
	key := claudeBindingKey("route-a", "claude")
	want := claudeControlIntent{
		Owner:          claudeOwnerRemote,
		BindingKey:     key,
		ConversationID: buildClaudeConversationID("route-a", "claude", workspace),
		Revision:       6,
	}
	data, err := json.Marshal(claudeSessionState{
		Version: claudeSessionStateVersion,
		Bindings: map[string]claudeSessionBinding{
			key: newClaudeBinding(workspace, "session-a", claudeBindingReady),
		},
		Controls: map[string]claudeControlIntent{"session-a": want},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	if got := store.controlIntent("session-a"); got != want {
		t.Fatalf("intent=%+v, want %+v", got, want)
	}
}

func TestClaudeSessionStoreLoadSerializesWithSave(t *testing.T) {
	store := newClaudeSessionStore()
	store.saveMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- store.load()
	}()
	<-started
	select {
	case err := <-done:
		store.saveMu.Unlock()
		t.Fatalf("load bypassed saveMu: %v", err)
	default:
	}
	store.saveMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHandlerSetClaudeSessionFileSerializesWithSave(t *testing.T) {
	h := &Handler{}
	store := h.ensureClaudeSessions()
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	store.saveMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- h.SetClaudeSessionFile(path)
	}()
	<-started
	select {
	case err := <-done:
		store.saveMu.Unlock()
		t.Fatalf("SetClaudeSessionFile bypassed saveMu: %v", err)
	default:
	}
	store.saveMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSessionStoreBindingUpdatePreservesControls(t *testing.T) {
	store := newClaudeSessionStore()
	store.controls["session-local"] = claudeControlIntent{Owner: claudeOwnerLocal, Revision: 7}
	var persisted claudeSessionState
	store.persist = func(state claudeSessionState) error {
		persisted = state
		return nil
	}
	if err := store.commitSelection("route", "/tmp/project", "session-new"); err != nil {
		t.Fatal(err)
	}
	want := claudeControlIntent{Owner: claudeOwnerLocal, Revision: 7}
	if got := store.controlIntent("session-local"); got != want {
		t.Fatalf("memory control=%+v, want %+v", got, want)
	}
	if got := persisted.Controls["session-local"]; got != want {
		t.Fatalf("persisted control=%+v, want %+v", got, want)
	}
}

func TestClaudeSessionStoreSaveFailureRollsBackBindingsAndControls(t *testing.T) {
	store := newClaudeSessionStore()
	if err := store.commitSelection("route", "/tmp/old", "session-old"); err != nil {
		t.Fatal(err)
	}
	wantControl := claudeControlIntent{Owner: claudeOwnerLocal, Revision: 8}
	store.mu.Lock()
	store.controls["session-local"] = wantControl
	store.mu.Unlock()
	store.persist = func(state claudeSessionState) error {
		state.Controls["session-local"] = claudeControlIntent{Owner: claudeOwnerRemote, Revision: 99}
		return errors.New("disk full")
	}
	if err := store.commitSelection("route", "/tmp/new", "session-new"); err == nil {
		t.Fatal("commitSelection error=nil, want save failure")
	}
	if got := store.binding("route"); got.WorkspaceRoot != "/tmp/old" || got.SessionID != "session-old" {
		t.Fatalf("binding=%+v, want original binding", got)
	}
	if got := store.controlIntent("session-local"); got != wantControl {
		t.Fatalf("control=%+v, want %+v", got, wantControl)
	}
}

func TestClaudeSessionStoreConcurrentLoadDoesNotOverwriteUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	key := claudeBindingKey("route", "claude")
	initial := newClaudeSessionState(
		map[string]claudeSessionBinding{key: newClaudeBinding("/tmp/old", "session-old", claudeBindingReady)},
		map[string]claudeControlIntent{"session-old": {Owner: claudeOwnerLocal, Revision: 1}},
	)
	if err := persistClaudeSessionState(path, initial); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}

	store.saveMu.Lock()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	loadStarted := make(chan struct{})
	updateStarted := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		close(loadStarted)
		errs <- store.load()
	}()
	go func() {
		defer wg.Done()
		close(updateStarted)
		errs <- store.commitSelection(key, "/tmp/new", "session-new")
	}()
	<-loadStarted
	<-updateStarted
	store.saveMu.Unlock()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	reloaded := newClaudeSessionStore()
	if err := reloaded.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.binding(key); got.SessionID != store.binding(key).SessionID {
		t.Fatalf("disk session=%q memory session=%q", got.SessionID, store.binding(key).SessionID)
	}
}

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
