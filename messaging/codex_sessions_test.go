package messaging

import (
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
