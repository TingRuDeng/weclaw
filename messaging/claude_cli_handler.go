package messaging

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func (h *Handler) handleClaudeCLI(route claudeSessionRoute) string {
	workspaceRoot := h.claudeWorkspaceRootForUser(route.UserID, route.AgentName, route.Agent)
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	if binding.Status != claudeBindingReady || strings.TrimSpace(binding.SessionID) == "" {
		return "当前还没有可接手的 Claude session，请先发送 /cc ls 选择或 /cc new 新建。"
	}
	command := strings.TrimSpace(route.Agent.Info().Command)
	if command == "" {
		return "当前 Claude Agent 未配置 command，无法打开 Claude CLI。"
	}
	if err := h.resolveClaudeCLIResumeOpener()(route.Context, command, workspaceRoot, binding.SessionID); err != nil {
		return fmt.Sprintf("打开 Claude CLI 失败: %v", err)
	}
	return wechatCommandText("已打开 Claude CLI。", "工作空间: "+workspaceRoot)
}

func (h *Handler) resolveClaudeCLIResumeOpener() ClaudeCLIResumeOpener {
	h.mu.RLock()
	opener := h.claudeCLIResumeOpener
	h.mu.RUnlock()
	if opener == nil {
		return defaultClaudeCLIResumeOpener
	}
	return opener
}

// defaultClaudeCLIResumeOpener 在 Terminal 中恢复当前 Claude session。
func defaultClaudeCLIResumeOpener(ctx context.Context, command string, workspaceRoot string, sessionID string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("当前平台 %s 暂不支持自动打开可见终端", runtime.GOOS)
	}
	parts := []string{
		"cd", shellQuoteForTerminal(workspaceRoot), "&&",
		shellQuoteForTerminal(command), "--resume", shellQuoteForTerminal(sessionID),
	}
	script := "tell application \"Terminal\" to do script " + appleScriptQuoteForTerminal(strings.Join(parts, " "))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
