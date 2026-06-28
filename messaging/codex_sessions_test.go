package messaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSessionStorePersistsWorkspaceThreads(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	bindingKey := codexBindingKey("user-1", "codex")
	workspace := filepath.Join(t.TempDir(), "project")

	first := newCodexSessionStore()
	first.SetFilePath(stateFile)
	first.setThread(bindingKey, workspace, "thread-1")

	second := newCodexSessionStore()
	second.SetFilePath(stateFile)

	threadID, pending := second.getThread(bindingKey, workspace)
	if threadID != "thread-1" || pending {
		t.Fatalf("restored thread=%q pending=%v, want thread-1 false", threadID, pending)
	}
	views := second.listWorkspaces(bindingKey)
	if len(views) != 1 || views[0].WorkspaceRoot != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("restored workspaces=%#v, want one normalized workspace", views)
	}
}

func TestCodexSessionStorePersistsPendingNewThread(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	bindingKey := codexBindingKey("user-1", "codex")
	workspace := filepath.Join(t.TempDir(), "project")

	first := newCodexSessionStore()
	first.SetFilePath(stateFile)
	first.setPendingNew(bindingKey, workspace)

	second := newCodexSessionStore()
	second.SetFilePath(stateFile)

	threadID, pending := second.getThread(bindingKey, workspace)
	if threadID != "" || !pending {
		t.Fatalf("restored thread=%q pending=%v, want empty true", threadID, pending)
	}
}

func TestCodexSessionStorePersistsActiveWorkspace(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	bindingKey := codexBindingKey("user-1", "codex")
	workspace := filepath.Join(t.TempDir(), "project")

	first := newCodexSessionStore()
	first.SetFilePath(stateFile)
	first.setThread(bindingKey, workspace, "thread-1")
	first.setActiveWorkspace(bindingKey, workspace)

	second := newCodexSessionStore()
	second.SetFilePath(stateFile)

	active, ok := second.getActiveWorkspace(bindingKey)
	if !ok || active != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("active workspace=(%q,%v), want %q true", active, ok, normalizeCodexWorkspaceRoot(workspace))
	}
}

func TestCodexSessionStoreKeepsThreadBoundToOneWorkspace(t *testing.T) {
	bindingKey := codexBindingKey("user-1", "codex")
	firstWorkspace := filepath.Join(t.TempDir(), "first")
	secondWorkspace := filepath.Join(t.TempDir(), "second")
	store := newCodexSessionStore()

	store.setThread(bindingKey, firstWorkspace, "thread-1")
	store.setThread(bindingKey, secondWorkspace, "thread-1")

	firstThread, firstPending := store.getThread(bindingKey, firstWorkspace)
	if firstThread != "" || firstPending {
		t.Fatalf("旧 workspace 不应继续绑定 thread，thread=%q pending=%v", firstThread, firstPending)
	}
	secondThread, secondPending := store.getThread(bindingKey, secondWorkspace)
	if secondThread != "thread-1" || secondPending {
		t.Fatalf("新 workspace thread=%q pending=%v，want thread-1 false", secondThread, secondPending)
	}
	owner, ok := store.findWorkspaceByThread(bindingKey, "thread-1")
	if !ok || owner != normalizeCodexWorkspaceRoot(secondWorkspace) {
		t.Fatalf("thread owner=(%q,%v)，want %q true", owner, ok, normalizeCodexWorkspaceRoot(secondWorkspace))
	}
}

func TestCodexSessionStoreMigratesLegacyWeChatBindingKey(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	workspace := filepath.Join(t.TempDir(), "project")
	writeCodexSessionState(t, stateFile, codexSessionState{
		Version: 1,
		Bindings: map[string]codexSessionBinding{
			"user-1\x00codex": {
				ActiveWorkspace: workspace,
				Workspaces: map[string]codexWorkspaceSession{
					workspace: {ThreadID: "thread-1"},
				},
			},
		},
	})

	store := newCodexSessionStore()
	store.SetFilePath(stateFile)

	bindingKey := codexBindingKey("wechat:user-1", "codex")
	threadID, pending := store.getThread(bindingKey, workspace)
	if threadID != "thread-1" || pending {
		t.Fatalf("migrated thread=%q pending=%v，want thread-1 false", threadID, pending)
	}
	assertCodexStateHasOnlyBinding(t, stateFile, bindingKey)
}

func TestCodexSessionStoreDoesNotDoublePrefixMigratedBindingKey(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	workspace := filepath.Join(t.TempDir(), "project")
	bindingKey := codexBindingKey("wechat:user-1", "codex")
	writeCodexSessionState(t, stateFile, codexSessionState{
		Version: 1,
		Bindings: map[string]codexSessionBinding{
			bindingKey: {
				Workspaces: map[string]codexWorkspaceSession{
					workspace: {ThreadID: "thread-1"},
				},
			},
		},
	})

	store := newCodexSessionStore()
	store.SetFilePath(stateFile)

	threadID, pending := store.getThread(bindingKey, workspace)
	if threadID != "thread-1" || pending {
		t.Fatalf("thread=%q pending=%v，want thread-1 false", threadID, pending)
	}
	assertCodexStateHasOnlyBinding(t, stateFile, bindingKey)
}

func writeCodexSessionState(t *testing.T, stateFile string, state codexSessionState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(stateFile, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func assertCodexStateHasOnlyBinding(t *testing.T, stateFile string, bindingKey string) {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state codexSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if len(state.Bindings) != 1 {
		t.Fatalf("bindings=%#v，want only %q", state.Bindings, bindingKey)
	}
	if _, ok := state.Bindings[bindingKey]; !ok {
		t.Fatalf("missing binding %q in %#v", bindingKey, state.Bindings)
	}
}
