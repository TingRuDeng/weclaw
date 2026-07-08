package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCwdAllowlistRejectsOutsideRoots(t *testing.T) {
	root := t.TempDir()
	allowedSub := filepath.Join(root, "project")
	if err := os.MkdirAll(allowedSub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() // 另一个不在白名单内的目录

	h := NewHandler(nil, nil)
	h.SetAllowedWorkspaceRoots([]string{root})

	if got := h.handleCwd("/cwd " + allowedSub); strings.Contains(got, "不在允许") {
		t.Fatalf("expected allowed path accepted, got %q", got)
	}
	if got := h.handleCwd("/cwd " + outside); !strings.Contains(got, "不在允许") {
		t.Fatalf("expected outside path rejected, got %q", got)
	}
}

func TestCwdAllowlistEmptyRejectsDirectorySwitch(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil)
	if got := h.handleCwd("/cwd " + dir); !strings.Contains(got, "allowed_workspace_roots") {
		t.Fatalf("empty allowlist should reject /cwd switch, got %q", got)
	}
}
