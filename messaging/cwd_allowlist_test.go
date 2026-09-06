package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

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
		{name: "路径不存在", path: filepath.Join(t.TempDir(), "missing"), want: "路径不存在"},
		{name: "目标是文件", path: filePath, want: "路径不是目录"},
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

func TestCwdAuthorizedUserCanSwitchToAnyDirectory(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil)

	got := h.handleCwdForMessage("/cwd "+dir, authorizeIncomingMessageForTest(t, platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "wx_admin",
	}, "wx_admin"), "")

	if !strings.Contains(got, "工作目录: "+canonicalTestPath(t, dir)) {
		t.Fatalf("authorized user should switch cwd, got %q", got)
	}
}

func TestCwdFeishuAuthorizedUnionIDCanSwitchDirectory(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil)

	got := h.handleCwdForMessage("/cwd "+dir, authorizeIncomingMessageForTest(t, platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_admin",
		Metadata: map[string]string{"feishu_union_id": "on_admin"},
	}, "on_admin"), "")

	if !strings.Contains(got, "工作目录: "+canonicalTestPath(t, dir)) {
		t.Fatalf("authorized Feishu union_id should switch cwd, got %q", got)
	}
}
