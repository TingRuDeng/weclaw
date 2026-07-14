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

// TestResolveCwdPathReturnsCanonicalPath 验证工作目录校验后使用真实路径而不是符号链接入口。
func TestResolveCwdPathReturnsCanonicalPath(t *testing.T) {
	realPath := t.TempDir()
	linkPath := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}

	got, err := resolveCwdPath(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalTestPath(t, realPath)
	if got != want {
		t.Fatalf("resolved path=%q, want %q", got, want)
	}
}

// TestResolveCwdPathRejectsInvalidTargets 验证不存在路径和普通文件不能成为工作空间。
func TestResolveCwdPathRejectsInvalidTargets(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(filePath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "路径不存在", path: filepath.Join(t.TempDir(), "missing"), want: "Path not found"},
		{name: "目标是文件", path: filePath, want: "Not a directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveCwdPath(test.path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v，期望包含 %q", err, test.want)
			}
		})
	}
}

// TestResolveCwdPathExpandsHome 验证主目录缩写在规范化前正确展开。
func TestResolveCwdPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	child := filepath.Join(home, "project")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	tests := map[string]string{
		"~":         canonicalTestPath(t, home),
		"~/project": canonicalTestPath(t, child),
	}
	for input, want := range tests {
		got, err := resolveCwdPath(input)
		if err != nil {
			t.Fatalf("resolve %q: %v", input, err)
		}
		if got != want {
			t.Fatalf("resolve %q=%q, want %q", input, got, want)
		}
	}
}

// TestCwdAllowlistRejectsSymlinkEscape 验证普通用户不能通过白名单内的链接进入外部目录。
func TestCwdAllowlistRejectsSymlinkEscape(t *testing.T) {
	allowedRoot := t.TempDir()
	outside := t.TempDir()
	linkPath := filepath.Join(allowedRoot, "outside-link")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
	h.SetAllowedWorkspaceRoots([]string{allowedRoot})

	if got := h.handleCwd("/cwd " + linkPath); !strings.Contains(got, "不在允许") {
		t.Fatalf("符号链接越界应被拒绝，got %q", got)
	}
}

// TestConfiguredAgentWorkspaceMatchesCanonicalPath 验证配置中的链接路径与真实会话路径属于同一工作空间。
func TestConfiguredAgentWorkspaceMatchesCanonicalPath(t *testing.T) {
	realPath := t.TempDir()
	linkPath := filepath.Join(t.TempDir(), "configured-link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
	h.SetAgentWorkDirs(map[string]string{"codex": linkPath})

	if !h.isConfiguredAgentWorkspace("codex", realPath) {
		t.Fatalf("configured workspace %q should match real path %q", linkPath, realPath)
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

	if !strings.Contains(got, "cwd: "+canonicalTestPath(t, dir)) {
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

	if !strings.Contains(got, "cwd: "+canonicalTestPath(t, dir)) {
		t.Fatalf("feishu admin should bypass empty allowed_workspace_roots by union_id, got %q", got)
	}
}
