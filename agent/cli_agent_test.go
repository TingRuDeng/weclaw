package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCLIAgentRejectsClaudeBackend(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "/不存在/claude"})
	_, err := ag.Chat(context.Background(), "conversation-1", "hello")
	if err == nil || !strings.Contains(err.Error(), "Claude 必须使用 ACP") {
		t.Fatalf("err=%v, want explicit ACP-only rejection", err)
	}
}

// TestCLIAgentTurnTimeoutKillsHangingProcess 验证单轮超时会在宽限期内中止卡死命令并返回错误。
func TestCLIAgentTurnTimeoutKillsHangingProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill semantics differ on windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hang.sh")
	// 脚本派生一个子进程并自身长睡，模拟卡死的 bash/测试命令。
	content := "#!/bin/sh\nsleep 30 &\nsleep 30\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	agent := NewCLIAgent(CLIAgentConfig{Name: "codex", Command: script, Cwd: dir})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := agent.Chat(ctx, "conv-timeout", "hello")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when turn times out")
	}
	if elapsed > turnKillGrace+3*time.Second {
		t.Fatalf("turn was not bounded by timeout+grace: took %s", elapsed)
	}
}

// TestConfigureProcessGroupSetsPgid 验证单轮子进程被置于独立进程组以便整组回收。
func TestConfigureProcessGroupSetsPgid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no process group on windows")
	}
	cmd := exec.Command("true")
	configureTurnProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("expected Setpgid to be enabled for turn process")
	}
	if cmd.Cancel == nil {
		t.Fatal("expected graceful Cancel to be set")
	}
	if cmd.WaitDelay != turnKillGrace {
		t.Fatalf("expected WaitDelay=%s, got %s", turnKillGrace, cmd.WaitDelay)
	}
}

func TestCLIAgentConversationCwdOverridesGlobalCwd(t *testing.T) {
	dir := t.TempDir()
	workspaceA := filepath.Join(dir, "workspace-a")
	workspaceB := filepath.Join(dir, "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatalf("mkdir workspace A: %v", err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("mkdir workspace B: %v", err)
	}
	recordPath := filepath.Join(dir, "pwd.txt")
	scriptPath := filepath.Join(dir, "fake-codex")
	script := "#!/bin/sh\npwd > " + shellQuoteForTest(recordPath) + "\necho ok\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	ag := NewCLIAgent(CLIAgentConfig{Name: "codex", Command: scriptPath, Cwd: workspaceB})
	ag.SetConversationCwd("conversation-a", workspaceA)
	ag.SetCwd(workspaceB)

	if _, err := ag.Chat(context.Background(), "conversation-a", "hello"); err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read recorded pwd: %v", err)
	}
	if got := string(recorded); got != workspaceA+"\n" {
		t.Fatalf("pwd=%q, want %q", got, workspaceA+"\n")
	}
}

func shellQuoteForTest(value string) string {
	quoted := "'"
	for _, r := range value {
		if r == '\'' {
			quoted += "'\\''"
			continue
		}
		quoted += string(r)
	}
	return quoted + "'"
}
