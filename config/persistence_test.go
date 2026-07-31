package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadReturnsConfigPathError 验证无法确定配置目录时不会静默使用默认配置。
func TestLoadReturnsConfigPathError(t *testing.T) {
	t.Setenv("WECLAW_HOME", "")
	t.Setenv("HOME", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "config path") {
		t.Fatalf("Load() error=%v, want config path error", err)
	}
}

// TestLoadMissingFileAppliesEnvironment 验证配置文件不存在时仍应用环境变量覆盖。
func TestLoadMissingFileAppliesEnvironment(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	t.Setenv("WECLAW_DEFAULT_AGENT", "claude")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultAgent != "claude" || cfg.Progress.Mode == "" {
		t.Fatalf("default_agent=%q progress=%#v", cfg.DefaultAgent, cfg.Progress)
	}
}

// TestLoadRejectsMalformedConfig 验证损坏 JSON 会返回明确错误。
func TestLoadRejectsMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WECLAW_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("Load() error=%v, want parse config error", err)
	}
}

// TestLoadValidatesFileConfig 验证加载后会执行配置校验。
func TestLoadValidatesFileConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WECLAW_HOME", dir)
	data := []byte(`{"agents":{"codex":{"max_history":-1}}}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "max_history") {
		t.Fatalf("Load() error=%v, want validation error", err)
	}
}

// TestLoadEnvironmentOverridesNormalizedFile 验证环境变量覆盖发生在文件默认值补齐之后。
func TestLoadEnvironmentOverridesNormalizedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WECLAW_HOME", dir)
	t.Setenv("WECLAW_PROGRESS_MODE", "typing")
	data := []byte(`{"progress":{"mode":"stream"}}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Progress.Mode != "typing" || cfg.Progress.TypingHeartbeatSeconds == 0 {
		t.Fatalf("progress=%#v", cfg.Progress)
	}
}

func TestLoadRejectsBroadConfigPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WECLAW_HOME", dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "0600") ||
		!strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("Load() error=%v, want actionable private-mode rejection", err)
	}
}

func TestLoadRejectsConfigSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WECLAW_HOME", dir)
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"agents":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted config symlink")
	}
}

func TestLoadRejectsSymlinkDataDir(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "weclaw-home")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECLAW_HOME", link)

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("Load() error=%v, want data-dir symlink rejection", err)
	}
}
