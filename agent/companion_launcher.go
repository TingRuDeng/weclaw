package agent

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// LaunchCompanionTerminal 在本机可见终端中启动 companion；非 macOS 平台暂不自动弹窗。
func LaunchCompanionTerminal(ctx context.Context, req CompanionLaunchRequest) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("当前平台 %s 暂不支持自动打开可见终端", runtime.GOOS)
	}
	script := "tell application \"Terminal\" to do script " + appleScriptQuote(companionShellCommand(req))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Terminal 失败: %w", err)
	}
	return cmd.Process.Release()
}

func companionShellCommand(req CompanionLaunchRequest) string {
	return shellQuote(req.Executable) + " companion --agent " + shellQuote(req.Agent) + " --cwd " + shellQuote(req.Cwd)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func appleScriptQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}
