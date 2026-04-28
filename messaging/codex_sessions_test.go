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
