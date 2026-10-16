//go:build darwin

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

func TestRestoreCodexAppEnvironmentPreservesLaterUserChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch-environment.json")
	backup := codexAppSharedLaunchBackup{
		Previous: map[string]string{codexCLIPathEnv: "/prior/codex", codexAppForceCLIEnv: "", codexAppCodexHomeEnv: "/prior/home"},
		Applied:  map[string]string{codexCLIPathEnv: "/weclaw/bridge", codexAppForceCLIEnv: "1", codexAppCodexHomeEnv: "/weclaw/home"},
	}
	data, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := securefile.Write(path, data); err != nil {
		t.Fatal(err)
	}
	current := map[string]string{codexCLIPathEnv: "/weclaw/bridge", codexAppForceCLIEnv: "1", codexAppCodexHomeEnv: "/user/changed"}
	writes := map[string][]string{}
	err = restoreCodexAppSharedEnvironmentWithDeps(context.Background(), dir,
		func(_ context.Context, key string) (string, error) { return current[key], nil },
		func(_ context.Context, args ...string) (string, error) { writes[args[1]] = args; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		codexCLIPathEnv:     {"setenv", codexCLIPathEnv, "/prior/codex"},
		codexAppForceCLIEnv: {"unsetenv", codexAppForceCLIEnv},
	}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("writes=%v, want %v", writes, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup should be removed after success: %v", err)
	}
}

func TestRestoreCodexAppEnvironmentRetainsBackupOnFailure(t *testing.T) {
	for _, kind := range []string{"read", "write", "invalid", "missing prior", "unknown key"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "launch-environment.json")
			data := `{"previous":{"CODEX_CLI_PATH":""},"applied":{"CODEX_CLI_PATH":"bridge"}}`
			switch kind {
			case "invalid":
				data = `{}`
			case "missing prior":
				data = `{"previous":{},"applied":{"CODEX_CLI_PATH":"bridge"}}`
			case "unknown key":
				data = `{"previous":{"OTHER":""},"applied":{"OTHER":"x"}}`
			}
			if err := securefile.Write(path, []byte(data)); err != nil {
				t.Fatal(err)
			}
			writes := 0
			err := restoreCodexAppSharedEnvironmentWithDeps(context.Background(), dir,
				func(context.Context, string) (string, error) {
					if kind == "read" {
						return "", errors.New("read failed")
					}
					return "bridge", nil
				}, func(context.Context, ...string) (string, error) { writes++; return "", errors.New("write failed") })
			if err == nil {
				t.Fatal("expected failure")
			}
			if kind != "write" && writes != 0 {
				t.Fatal("invalid backup or failed read must not change environment")
			}
			remaining, err := securefile.Read(path)
			if err != nil || string(remaining) != data {
				t.Fatalf("backup lost: %v", err)
			}
		})
	}
}

func TestRestoreCodexAppEnvironmentWithoutBackupDoesNothing(t *testing.T) {
	err := restoreCodexAppSharedEnvironmentWithDeps(context.Background(), t.TempDir(),
		func(context.Context, string) (string, error) { t.Fatal("unexpected read"); return "", nil },
		func(context.Context, ...string) (string, error) { t.Fatal("unexpected write"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
}
