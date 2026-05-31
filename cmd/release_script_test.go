package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseScriptSyntaxAndHelp(t *testing.T) {
	script := releaseScriptPath(t)

	runReleaseScriptTestCommand(t, "", "bash", "-n", script)
	output := runReleaseScriptTestCommand(t, "", script, "--help")
	if !strings.Contains(output, "--next-patch") || !strings.Contains(output, "--dry-run") {
		t.Fatalf("help output missing expected options: %s", output)
	}
}

func TestReleaseScriptNextPatchTag(t *testing.T) {
	script := releaseScriptPath(t)
	repo := t.TempDir()
	runReleaseScriptTestCommand(t, repo, "git", "init")
	runReleaseScriptTestCommand(t, repo, "git", "config", "user.email", "test@example.com")
	runReleaseScriptTestCommand(t, repo, "git", "config", "user.name", "测试用户")

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runReleaseScriptTestCommand(t, repo, "git", "add", "README.md")
	runReleaseScriptTestCommand(t, repo, "git", "commit", "-m", "初始化")
	runReleaseScriptTestCommand(t, repo, "git", "tag", "v0.1.9")
	runReleaseScriptTestCommand(t, repo, "git", "tag", "v0.1.10")

	output := runReleaseScriptTestCommand(t, repo, "bash", "-c", "WECLAW_RELEASE_SOURCE_ONLY=1 source "+shellQuote(script)+" && next_patch_tag")
	if strings.TrimSpace(output) != "v0.1.11" {
		t.Fatalf("next patch tag=%q, want v0.1.11", strings.TrimSpace(output))
	}
}

func releaseScriptPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "scripts", "release.sh"))
	if err != nil {
		t.Fatalf("resolve release script: %v", err)
	}
	return abs
}

func runReleaseScriptTestCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
