package agent

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestCLIAgentRejectsClaudeBackend(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "/不存在/claude"})
	_, err := ag.Chat(context.Background(), "conversation-1", "hello")
	if err == nil || !strings.Contains(err.Error(), "Claude 必须使用 ACP") {
		t.Fatalf("err=%v, want explicit ACP-only rejection", err)
	}
}

func TestCLIAgentRejectsLegacyCodexExecBackend(t *testing.T) {
	ag := NewCLIAgent(CLIAgentConfig{Name: "codex", Command: "codex"})
	_, err := ag.Chat(context.Background(), "conversation-1", "hello")
	if err == nil || !strings.Contains(err.Error(), "独立 exec 会话模式已停用") {
		t.Fatalf("err=%v, want explicit shared app-server migration", err)
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
