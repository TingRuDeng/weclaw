package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLookPath_InPath verifies that lookPath finds binaries already in PATH.
func TestLookPath_InPath(t *testing.T) {
	p, err := lookPath("ls")
	if err != nil {
		t.Fatalf("expected to find ls, got error: %v", err)
	}
	if p == "" {
		t.Fatal("expected non-empty path for ls")
	}
}

// TestLookPath_NotExist verifies that lookPath returns an error for missing binaries.
func TestLookPath_NotExist(t *testing.T) {
	_, err := lookPath("nonexistent-binary-xyz-12345")
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

// TestLookPath_LoginShellTimeout 验证登录 shell 兜底探测受超时限制。
func TestLookPath_LoginShellTimeout(t *testing.T) {
	// 使用不存在的二进制，确保进入登录 shell 兜底路径。
	const missing = "nonexistent-binary-timeout-xyz"

	origTimeout := loginShellLookupTimeout
	origCommand := loginShellWhichCommand
	t.Cleanup(func() {
		loginShellLookupTimeout = origTimeout
		loginShellWhichCommand = origCommand
	})

	loginShellLookupTimeout = 200 * time.Millisecond
	loginShellWhichCommand = func(ctx context.Context, _ string, _ string) *exec.Cmd {
		// 模拟 shell rc 卡住且远超探测超时时间。
		return exec.CommandContext(ctx, "sleep", "30")
	}

	start := time.Now()
	_, err := lookPath(missing)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when login shell times out")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("lookPath did not honor login-shell timeout: took %s", elapsed)
	}
}

// TestLoginShellWhichCommandPassesBinaryAsArg 验证登录 shell 兜底探测不把用户配置的 command 拼进脚本文本。
func TestLoginShellWhichCommandPassesBinaryAsArg(t *testing.T) {
	const binary = "missing; echo injected"

	cmd := loginShellWhichCommand(context.Background(), "zsh", binary)

	if len(cmd.Args) < 5 {
		t.Fatalf("login shell args=%#v, want script plus binary argument", cmd.Args)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != binary {
		t.Fatalf("binary arg=%q, want %q; args=%#v", got, binary, cmd.Args)
	}
	script := cmd.Args[3]
	if script == "" || script == "which "+binary {
		t.Fatalf("script=%q, want parameterized command lookup", script)
	}
}

// TestLookPath_LoginShellFallback reproduces the daemon scenario:
// PATH is stripped to system-only dirs (no nvm), so exec.LookPath fails,
// but lookPath resolves claude via login shell fallback.
func TestLookPath_LoginShellFallback(t *testing.T) {
	// Precondition: claude must be discoverable via login shell (i.e. nvm in .zshrc)
	fullPath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not installed, skipping login shell fallback test")
	}

	// Simulate daemon environment: strip PATH to system-only dirs
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	defer os.Setenv("PATH", origPath)

	// Reproduce the bug: exec.LookPath must fail under stripped PATH
	_, err = exec.LookPath("claude")
	if err == nil {
		t.Skip("claude found in minimal PATH, cannot reproduce nvm issue")
	}

	// Verify fix: lookPath should find claude via login shell
	p, err := lookPath("claude")
	if err != nil {
		t.Fatalf("lookPath should find claude via login shell, got: %v", err)
	}
	if p != fullPath {
		t.Logf("resolved path differs: direct=%s, login-shell=%s (acceptable)", fullPath, p)
	}
	t.Logf("lookPath resolved claude via login shell: %s", p)
}

// TestDetectAndConfigure_StrippedPath is an end-to-end test:
// empty config + stripped PATH → DetectAndConfigure should still find claude.
func TestDetectAndConfigure_StrippedPath(t *testing.T) {
	withAgentDetection(t, map[string]string{"claude": "/fake/bin/claude"}, nil)

	cfg := DefaultConfig()
	DetectAndConfigure(cfg)

	agent, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatal("expected claude to be detected via login shell fallback")
	}
	if agent.Type != "cli" {
		t.Fatalf("expected type=cli, got %s", agent.Type)
	}
	if agent.Command != "/fake/bin/claude" {
		t.Fatalf("detected claude command=%s, want fake path", agent.Command)
	}
}

func TestDetectAndConfigureOpenCodeUsesCompanion(t *testing.T) {
	withAgentDetection(t, map[string]string{"opencode": "/fake/bin/opencode"}, nil)

	cfg := DefaultConfig()
	DetectAndConfigure(cfg)

	agent, ok := cfg.Agents["opencode"]
	if !ok {
		t.Fatal("expected opencode to be detected")
	}
	if agent.Type != "companion" {
		t.Fatalf("opencode type = %q, want companion", agent.Type)
	}
}

func TestOpenclawACPConfigKeepsSecretsOutOfArgs(t *testing.T) {
	cfg := openclawACPConfig("openclaw", "ws://127.0.0.1:18789", "secret-token", "")
	joined := strings.Join(cfg.Args, " ")
	if strings.Contains(joined, "secret-token") || strings.Contains(joined, "--token") {
		t.Fatalf("args expose token: %#v", cfg.Args)
	}
	if cfg.Env["OPENCLAW_GATEWAY_TOKEN"] != "secret-token" {
		t.Fatalf("env=%#v, want gateway token", cfg.Env)
	}
	passwordCfg := openclawACPConfig("openclaw", "ws://127.0.0.1:18789", "", "secret-password")
	if strings.Contains(strings.Join(passwordCfg.Args, " "), "secret-password") ||
		passwordCfg.Env["OPENCLAW_GATEWAY_PASSWORD"] != "secret-password" {
		t.Fatalf("password config=%#v, want secret only in env", passwordCfg)
	}
}

func withAgentDetection(t *testing.T, paths map[string]string, probes map[string]bool) {
	t.Helper()
	oldLookPath := detectLookPath
	oldCommandProbe := detectCommandProbe
	detectLookPath = func(binary string) (string, error) {
		path, ok := paths[binary]
		if !ok {
			return "", fmt.Errorf("not found: %s", binary)
		}
		return path, nil
	}
	detectCommandProbe = func(binary string, args []string) bool {
		key := binary + "\x00" + fmt.Sprint(args)
		allowed, ok := probes[key]
		return !ok || allowed
	}
	t.Cleanup(func() {
		detectLookPath = oldLookPath
		detectCommandProbe = oldCommandProbe
	})
}
