package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransformCodexRolloutProviderPreservesVisibleHistory(t *testing.T) {
	reference := map[string]any{"call_item_id": "item_call"}
	replaceCodexItemReferences(reference, map[string]string{"item_call": "fc_call"}, "")
	if reference["call_item_id"] != "fc_call" {
		t.Fatalf("replaceCodexItemReferences() = %#v", reference)
	}
	threadID := "019fe000-1111-7222-8333-444444444444"
	original := strings.Join([]string{
		`{"timestamp":"2026-08-09T00:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `","model_provider":"relay"}}`,
		`{"timestamp":"2026-08-09T00:00:01Z","type":"response_item","payload":{"type":"message","id":"item_user","role":"user","content":[{"type":"input_text","text":"keep user"}]}}`,
		`{"timestamp":"2026-08-09T00:00:02Z","type":"response_item","payload":{"type":"reasoning","id":"item_secret","encrypted_content":"provider-bound"}}`,
		`{"timestamp":"2026-08-09T00:00:03Z","type":"response_item_reference","payload":{"item_id":"item_secret"}}`,
		`{"timestamp":"2026-08-09T00:00:04Z","type":"response_item","payload":{"type":"function_call","id":"item_call","name":"shell","arguments":"{}"}}`,
		`{"timestamp":"2026-08-09T00:00:05Z","type":"response_item","payload":{"type":"function_call_output","id":"item_output","call_item_id":"item_call","output":"ok"}}`,
		`{"timestamp":"2026-08-09T00:00:06Z","type":"response_item","payload":{"type":"message","id":"item_assistant","role":"assistant","content":[{"type":"output_text","text":"keep assistant"}]}}`,
	}, "\n") + "\n"

	transformed, result, err := transformCodexRolloutProvider([]byte(original), threadID, "openai")
	if err != nil {
		t.Fatalf("transformCodexRolloutProvider() error = %v", err)
	}
	if result.DeletedProviderBoundItems != 1 || result.RepairedItemIDs != 4 {
		t.Fatalf("transform result = %+v", result)
	}
	text := string(transformed)
	for _, want := range []string{"keep user", "keep assistant", `"model_provider":"openai"`, `"id":"fc_call"`, `"call_item_id":"fc_call"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("transformed rollout missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"provider-bound", "item_secret", "item_call", "item_output", "item_user", "item_assistant"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("transformed rollout contains %q:\n%s", forbidden, text)
		}
	}
	for lineNumber, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d invalid JSON: %v", lineNumber+1, err)
		}
	}
}

func TestTransformCodexRolloutProviderRejectsUnexpectedEncryptedContent(t *testing.T) {
	original := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread-1\",\"model_provider\":\"relay\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"msg_1\",\"encrypted_content\":\"unexpected\"}}\n")
	if _, _, err := transformCodexRolloutProvider(original, "thread-1", "openai"); err == nil || !strings.Contains(err.Error(), "encrypted_content") {
		t.Fatalf("transformCodexRolloutProvider() error = %v", err)
	}
}

func TestTransformCodexRolloutProviderSanitizesCompactedReplacementHistory(t *testing.T) {
	original := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"thread-1","model_provider":"relay"}}`,
		`{"type":"compacted","payload":{"message":"keep summary","replacement_history":[{"type":"message","id":"item_user","role":"user","content":[{"type":"input_text","text":"keep nested user"}]},{"type":"reasoning","id":"item_secret","summary":[],"encrypted_content":"provider-bound"},{"type":"function_call","id":"item_call","name":"shell","arguments":"{}"},{"type":"function_call_output","id":"item_output","call_item_id":"item_call","output":"keep tool output"},{"type":"context_compaction","id":"item_compaction","encrypted_content":"provider-bound-compaction"}]}}`,
	}, "\n") + "\n"

	transformed, result, err := transformCodexRolloutProvider([]byte(original), "thread-1", "openai")
	if err != nil {
		t.Fatalf("transformCodexRolloutProvider() error = %v", err)
	}
	if result.DeletedProviderBoundItems != 2 || result.RepairedItemIDs != 3 {
		t.Fatalf("transform result = %+v", result)
	}
	text := string(transformed)
	for _, want := range []string{
		`"type":"compacted"`, "keep summary", "keep nested user", "keep tool output",
		`"id":"msg_user"`, `"id":"fc_call"`, `"id":"fco_output"`, `"call_item_id":"fc_call"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("transformed rollout missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"encrypted_content", "provider-bound", "item_secret", "item_compaction", "item_call"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("transformed rollout contains %q:\n%s", forbidden, text)
		}
	}
}

func TestReadRootCodexProviderSupportsCommentsAndLiteralStrings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("model_provider = 'relay-main' # active\n[profiles.other]\nmodel_provider = \"ignored\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, found, err := readRootCodexProvider(configPath)
	if err != nil || !found || provider != "relay-main" {
		t.Fatalf("readRootCodexProvider() = %q, %t, %v", provider, found, err)
	}
}

func TestResumeThreadWithProviderSendsAndVerifiesProvider(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.rpcCall = func(_ context.Context, method string, params any) (json.RawMessage, error) {
		if method != "thread/resume" {
			return nil, nil
		}
		values := params.(map[string]any)
		if values["modelProvider"] != "openai" {
			t.Fatalf("thread/resume params = %#v", values)
		}
		return json.RawMessage(`{"thread":{"id":"thread-1","modelProvider":"openai"}}`), nil
	}
	if err := a.resumeThreadWithProvider(context.Background(), "conversation-1", "thread-1", "openai"); err != nil {
		t.Fatalf("resumeThreadWithProvider() error = %v", err)
	}

	a.rpcCall = func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"thread":{"id":"thread-1","modelProvider":"relay"}}`), nil
	}
	if err := a.resumeThreadWithProvider(context.Background(), "conversation-1", "thread-1", "openai"); err == nil || !strings.Contains(err.Error(), "provider mismatch") {
		t.Fatalf("resumeThreadWithProvider() mismatch error = %v", err)
	}
}

func TestMigrateCodexThreadProviderUpdatesExactThreadAndCatalog(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	codexHome := t.TempDir()
	if err := os.Chmod(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	threadID := "019fe000-1111-7222-8333-444444444444"
	otherID := "019fe000-9999-7222-8333-444444444444"
	rolloutPath := filepath.Join(codexHome, "sessions", "2026", "08", "09", "rollout.jsonl")
	if err := os.MkdirAll(filepath.Dir(rolloutPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"type":"session_meta","payload":{"id":"` + threadID + `","model_provider":"relay"}}` + "\n")
	if err := os.WriteFile(rolloutPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	stateDB := filepath.Join(codexHome, "state_5.sqlite")
	catalogDB := filepath.Join(codexHome, "sqlite", "codex-dev.db")
	if err := os.MkdirAll(filepath.Dir(catalogDB), 0o700); err != nil {
		t.Fatal(err)
	}
	runSQLiteFixture(t, stateDB, `
CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL);
INSERT INTO threads VALUES ('`+threadID+`', '`+rolloutPath+`', 'relay');
INSERT INTO threads VALUES ('`+otherID+`', '`+rolloutPath+`', 'other');`)
	runSQLiteFixture(t, catalogDB, `
CREATE TABLE local_thread_catalog (host_id TEXT NOT NULL, thread_id TEXT NOT NULL, model_provider TEXT NOT NULL, PRIMARY KEY(host_id, thread_id));
INSERT INTO local_thread_catalog VALUES ('host-a', '`+threadID+`', 'relay');
INSERT INTO local_thread_catalog VALUES ('host-b', '`+threadID+`', 'relay-b');
INSERT INTO local_thread_catalog VALUES ('host-a', '`+otherID+`', 'other');`)

	result, err := migrateCodexThreadProvider(context.Background(), codexProviderMigrationRequest{
		CodexHome: codexHome, ThreadID: threadID, TargetProvider: "openai",
	})
	if err != nil {
		t.Fatalf("migrateCodexThreadProvider() error = %v", err)
	}
	if !result.Changed || result.PreviousProvider != "relay" || result.BackupDir == "" {
		t.Fatalf("migration result = %+v", result)
	}
	assertSQLiteScalar(t, stateDB, "SELECT model_provider FROM threads WHERE id='"+threadID+"';", "openai")
	assertSQLiteScalar(t, stateDB, "SELECT model_provider FROM threads WHERE id='"+otherID+"';", "other")
	assertSQLiteScalar(t, catalogDB, "SELECT group_concat(model_provider, ',') FROM (SELECT model_provider FROM local_thread_catalog WHERE thread_id='"+threadID+"' ORDER BY host_id);", "openai,openai")
	assertSQLiteScalar(t, catalogDB, "SELECT model_provider FROM local_thread_catalog WHERE thread_id='"+otherID+"';", "other")
	data, err := os.ReadFile(rolloutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"model_provider":"openai"`) {
		t.Fatalf("rollout = %s", data)
	}
	if info, err := os.Stat(rolloutPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("rollout mode = %v, err = %v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(filepath.Join(result.BackupDir, "rollout.jsonl")); err != nil {
		t.Fatalf("backup rollout missing: %v", err)
	}
	if err := markCodexProviderMigrationVerified(result.BackupDir); err != nil {
		t.Fatalf("markCodexProviderMigrationVerified() error = %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(result.BackupDir, "manifest.json"))
	if err != nil || !strings.Contains(string(manifestData), `"status": "verified"`) {
		t.Fatalf("manifest = %s, err = %v", manifestData, err)
	}
}

func TestPrepareCodexThreadUsesCurrentProviderAndDefersActiveMismatch(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	codexHome := t.TempDir()
	if err := os.Chmod(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	threadID := "thread-1"
	stateDB := filepath.Join(codexHome, "state_5.sqlite")
	runSQLiteFixture(t, stateDB, `CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, model_provider TEXT NOT NULL); INSERT INTO threads VALUES ('thread-1', '`+filepath.Join(codexHome, "rollout.jsonl")+`', 'relay');`)
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, Env: map[string]string{"CODEX_HOME": codexHome},
	})
	a.rpcCall = func(context.Context, string, any) (json.RawMessage, error) { return nil, nil }
	a.codexProviderReadCall = func(context.Context, string) (string, error) { return "openai", nil }
	a.setCodexRuntimeMode(CodexRuntimeWeClaw)

	result, err := a.PrepareCodexThread(context.Background(), CodexRuntimeRequest{
		Ref:        CodexThreadRef{ConversationID: "conversation-1", ThreadID: threadID},
		Checkpoint: CodexRolloutCheckpoint{Active: true}, WorkspaceRoot: t.TempDir(),
	})
	if err != nil || !result.Deferred || !result.TargetActive || result.Provider != "openai" || result.PreviousProvider != "relay" {
		t.Fatalf("PrepareCodexThread() = %+v, %v", result, err)
	}
	assertSQLiteScalar(t, stateDB, "SELECT model_provider FROM threads WHERE id='thread-1';", "relay")
}

func runSQLiteFixture(t *testing.T, database string, sql string) {
	t.Helper()
	cmd := exec.Command("sqlite3", database, sql)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite fixture: %v: %s", err, output)
	}
}

func assertSQLiteScalar(t *testing.T, database string, query string, want string) {
	t.Helper()
	output, err := exec.Command("sqlite3", database, query).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite query: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("sqlite scalar = %q, want %q", got, want)
	}
}
