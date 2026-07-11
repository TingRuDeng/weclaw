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

func TestReleaseScriptUpdateSmokeSkipsDryRun(t *testing.T) {
	script := releaseScriptPath(t)

	output := runReleaseScriptTestCommand(t, "", "bash", "-c", "WECLAW_RELEASE_SOURCE_ONLY=1 source "+shellQuote(script)+" && DRY_RUN=1 && verify_update_smoke && echo ok")
	if strings.TrimSpace(output) != "ok" {
		t.Fatalf("verify_update_smoke dry-run output=%q, want ok", output)
	}
}

func TestReleaseScriptUpdateSmokeSkipsUnsupportedHost(t *testing.T) {
	script := releaseScriptPath(t)
	command := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + ` && ` +
		`go() { if [[ "$1 $2" == "env GOHOSTOS" ]]; then echo linux; elif [[ "$1 $2" == "env GOHOSTARCH" ]]; then echo amd64; else echo unexpected-go-call >&2; return 9; fi; } && ` +
		`DRY_RUN=0 TAG=v9.9.9 verify_update_smoke`

	output := runReleaseScriptTestCommand(t, "", "bash", "-c", command)
	if !strings.Contains(output, "跳过 update smoke") {
		t.Fatalf("verify_update_smoke unsupported host output=%q, want skip hint", output)
	}
}

func TestReleaseWorkflowsOnlyBuildDarwinArm64(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", ".github", "workflows", "ci.yml"),
		filepath.Join("..", ".github", "workflows", "release.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		for _, unsupported := range []string{"goos: linux", "goos: windows", "goarch: amd64"} {
			if strings.Contains(text, unsupported) {
				t.Fatalf("%s contains unsupported release target %q", path, unsupported)
			}
		}
		if !strings.Contains(text, "goos: darwin") || !strings.Contains(text, "goarch: arm64") {
			t.Fatalf("%s missing darwin/arm64 target", path)
		}
	}
}

func TestReleaseValidationRunsGovulncheck(t *testing.T) {
	content, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatalf("read release script: %v", err)
	}
	if !strings.Contains(string(content), "govulncheck@v1.6.0") {
		t.Fatal("release validation must run pinned govulncheck v1.6.0")
	}
}

func TestWorkflowsUseSecureGoToolchainAndVulnerabilityScan(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", ".github", "workflows", "ci.yml"),
		filepath.Join("..", ".github", "workflows", "release.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		if !strings.Contains(text, "go-version: '1.26.5'") {
			t.Fatalf("%s must use Go 1.26.5", path)
		}
		if !strings.Contains(text, "govulncheck@v1.6.0") {
			t.Fatalf("%s must run pinned govulncheck v1.6.0", path)
		}
	}
}

func TestStableReleaseWorkflowIsManualOnlyAndBuildsRequestedTag(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "release.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取稳定版 workflow 失败：%v", err)
	}
	text := string(content)
	if strings.Contains(text, "\n  push:") {
		t.Fatal("稳定版 workflow 不应由 tag push 自动触发，本地发布脚本是唯一默认发布者")
	}
	if !strings.Contains(text, "workflow_dispatch:") {
		t.Fatal("稳定版 workflow 应保留手动兜底入口")
	}
	if !strings.Contains(text, "ref: ${{ inputs.tag }}") {
		t.Fatal("手动发布必须 checkout 输入 tag，禁止用默认分支内容冒充目标版本")
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
