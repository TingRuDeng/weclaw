package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeLocalCodexSession(t *testing.T, codexDir string, threadID string, workspace string, threadName string, updatedAt string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create local codex workspace: %v", err)
	}
	writeLocalCodexIndex(t, codexDir, threadID, threadName, updatedAt)
	writeLocalCodexSessionMeta(t, codexDir, threadID, workspace, updatedAt, `"Codex Desktop"`, `""`, `"vscode"`)
}

func writeCodexAppWorkspaceState(t *testing.T, codexDir string, projectOrder []string, savedRoots []string) {
	t.Helper()
	state := map[string][]string{
		"project-order":                  projectOrder,
		"electron-saved-workspace-roots": savedRoots,
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal codex app workspace state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, ".codex-global-state.json"), data, 0o600); err != nil {
		t.Fatalf("write codex app workspace state: %v", err)
	}
}

func writeFakeSQLite3(t *testing.T, output string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + output + "\nEOF\n"
	path := filepath.Join(binDir, "sqlite3")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sqlite3: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeLocalCodexIndex(t *testing.T, codexDir string, threadID string, threadName string, updatedAt string) {
	t.Helper()
	indexLine := fmt.Sprintf(`{"id":%q,"thread_name":%q,"updated_at":%q}`+"\n", threadID, threadName, updatedAt)
	indexPath := filepath.Join(codexDir, "session_index.jsonl")
	file, err := os.OpenFile(indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open session index: %v", err)
	}
	if _, err := file.WriteString(indexLine); err != nil {
		t.Fatalf("write session index: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close session index: %v", err)
	}
}

func writeLocalCodexSessionMeta(t *testing.T, codexDir string, threadID string, workspace string, updatedAt string, originatorJSON string, threadSourceJSON string, sourceJSON string) {
	t.Helper()
	sessionDir := filepath.Join(codexDir, "sessions", "2026", "04", "29")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "rollout-"+threadID+".jsonl")
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"originator":%s,"thread_source":%s,"source":%s}}`+"\n", updatedAt, threadID, updatedAt, workspace, originatorJSON, threadSourceJSON, sourceJSON)
	if err := os.WriteFile(sessionPath, []byte(meta), 0o600); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
}

func appendLocalCodexTurnContext(t *testing.T, codexDir string, threadID string, model string, effort string) {
	t.Helper()
	metas := readLocalCodexSessionMetas(filepath.Join(codexDir, "sessions"))
	meta, ok := metas[threadID]
	if !ok {
		t.Fatalf("find local codex session: %s", threadID)
	}
	record := fmt.Sprintf(`{"type":"turn_context","payload":{"model":%q,"effort":%q}}`+"\n", model, effort)
	file, err := os.OpenFile(meta.Path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open local codex session: %v", err)
	}
	if _, err := file.WriteString(record); err != nil {
		_ = file.Close()
		t.Fatalf("append local codex turn context: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close local codex session: %v", err)
	}
}

func writeArchivedLocalCodexSession(t *testing.T, codexDir string, threadID string, workspace string, threadName string, updatedAt string) {
	t.Helper()
	writeLocalCodexSession(t, codexDir, threadID, workspace, threadName, updatedAt)

	archivedDir := filepath.Join(codexDir, "archived_sessions")
	if err := os.MkdirAll(archivedDir, 0o700); err != nil {
		t.Fatalf("create archived session dir: %v", err)
	}
	meta := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"originator":"Codex Desktop"}}`+"\n", updatedAt, threadID, updatedAt, workspace)
	sessionPath := filepath.Join(archivedDir, "rollout-"+threadID+".jsonl")
	if err := os.WriteFile(sessionPath, []byte(meta), 0o600); err != nil {
		t.Fatalf("write archived session meta: %v", err)
	}
}
