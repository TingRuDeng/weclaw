package messaging

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

const maxClaudeSessionIDLength = 256

var claudeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// handleClaudeCLI 将已绑定且空闲的 ACP session 交接给原生 Claude CLI。
func (h *Handler) handleClaudeCLI(route claudeSessionRoute) string {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	defer unlock()
	binding := h.ensureClaudeSessions().binding(route.BindingKey)
	if binding.Status != claudeBindingReady || strings.TrimSpace(binding.SessionID) == "" {
		return "当前还没有可接手的 Claude session，请先发送 /cc ls 选择或 /cc new 新建。"
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
	if workspaceRoot == "" {
		return "当前 Claude session 缺少工作空间，请发送 /cc ls 重新选择。"
	}
	command := strings.TrimSpace(route.Agent.Info().LocalCommand)
	if command == "" {
		return "当前 Claude Agent 未配置 local_command，无法打开 Claude CLI。"
	}
	adminCtx := contextWithWorkspaceAdmin(route.Context, route.Admin)
	if !h.workspaceAllowedForAgentContext(adminCtx, route.AgentName, workspaceRoot) {
		return "当前工作空间不在允许范围，无法打开 Claude CLI。"
	}
	if !validClaudeSessionID(binding.SessionID) {
		return "当前 Claude session ID 非法，无法打开 Claude CLI。"
	}
	if h.hasActiveClaudeTask(route, workspaceRoot) {
		return "当前 Claude 任务正在运行，请等待任务结束或先发送 /stop。"
	}
	request := ClaudeCLIResumeRequest{Command: command, WorkspaceRoot: workspaceRoot, SessionID: binding.SessionID}
	if err := h.resolveClaudeCLIResumeOpener()(route.Context, request); err != nil {
		return fmt.Sprintf("打开 Claude CLI 失败: %v", err)
	}
	return wechatCommandText("已打开 Claude CLI。", "工作空间: "+workspaceRoot)
}

// hasActiveClaudeTask 同时兼容会话规范键和当前后台执行器实际登记键。
func (h *Handler) hasActiveClaudeTask(route claudeSessionRoute, workspaceRoot string) bool {
	conversationKey := buildClaudeConversationID(route.UserID, route.AgentName, workspaceRoot)
	if _, active := h.activeTask(conversationKey); active {
		return true
	}
	executionKey := h.agentExecutionKeyForRoute(route.ActorUserID, route.UserID, route.AgentName, route.Agent)
	if executionKey == conversationKey {
		return false
	}
	_, active := h.activeTask(executionKey)
	return active
}

// resolveClaudeCLIResumeOpener 返回测试注入实现或系统默认实现。
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
func defaultClaudeCLIResumeOpener(ctx context.Context, request ClaudeCLIResumeRequest) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("当前平台 %s 暂不支持自动打开可见终端", runtime.GOOS)
	}
	command := claudeCLIResumeCommand(request.Command, request.WorkspaceRoot, request.SessionID)
	script := "tell application \"Terminal\" to do script " + appleScriptQuoteForTerminal(command)
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// claudeCLIResumeCommand 使用独立原生参数构造终端命令，避免执行 ACP adapter。
func claudeCLIResumeCommand(command string, workspaceRoot string, sessionID string) string {
	parts := []string{
		"cd", shellQuoteForTerminal(workspaceRoot), "&&",
		shellQuoteForTerminal(command), "--resume", shellQuoteForTerminal(sessionID),
	}
	return strings.Join(parts, " ")
}

// validClaudeSessionID 拒绝空白、控制字符和 shell 元字符进入本地进程边界。
func validClaudeSessionID(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	return len(sessionID) <= maxClaudeSessionIDLength && claudeSessionIDPattern.MatchString(sessionID)
}
