package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
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

func TestCwdAdminBypassesWorkspaceRoots(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"wx_admin"})

	got := h.handleCwdForMessage("/cwd "+dir, platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "wx_admin",
	})

	if !strings.Contains(got, "cwd: "+dir) {
		t.Fatalf("admin should bypass empty allowed_workspace_roots, got %q", got)
	}
}

func TestCwdFeishuAdminUsesUnionIDBypass(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})

	got := h.handleCwdForMessage("/cwd "+dir, platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
	})

	if !strings.Contains(got, "cwd: "+dir) {
		t.Fatalf("feishu admin should bypass empty allowed_workspace_roots by union_id, got %q", got)
	}
}
