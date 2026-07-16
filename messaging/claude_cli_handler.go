package messaging

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

const maxClaudeSessionIDLength = 256

var (
	errClaudeCLIOpenFailed             = errors.New("打开 Claude CLI 失败")
	errClaudeCLIRemoteRestoreUncertain = errors.New("Claude CLI 失败后远程恢复未确认")
	errClaudeCLILocalHandoffUncertain  = errors.New("Claude CLI 本地交接未确认")
)

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
		return "当前 Claude 任务正在运行或已有暂存消息，请等待任务结束或先发送 /stop。"
	}
	if _, _, err := h.ensureClaudeSessions().requireRemoteControl(route.BindingKey); err != nil {
		return renderClaudeRemoteControlError(err)
	}
	if err := h.handoffClaudeSessionToCLIWithBindingLocked(route); err != nil {
		switch {
		case errors.Is(err, errClaudeCLIRemoteRestoreUncertain):
			return "打开 Claude CLI 失败；远程恢复未确认，已保持远程写入关闭，请检查状态后重试。"
		case errors.Is(err, errClaudeCLIOpenFailed):
			return "打开 Claude CLI 失败；已恢复远程控制，请稍后重试。"
		case errors.Is(err, errClaudeCLILocalHandoffUncertain):
			return "Claude CLI 交接未确认，已保持远程写入关闭，请检查状态后重试。"
		default:
			return renderClaudeOwnerMutationFailure(err)
		}
	}
	return wechatCommandText("已释放远程控制并打开 Claude CLI。", "工作空间: "+workspaceRoot)
}

// handoffClaudeSessionToCLIWithBindingLocked 在 binding 外层锁内完成
// remote -> local -> open；opener 失败时只通过统一 acquire 恢复远程控制。
func (h *Handler) handoffClaudeSessionToCLIWithBindingLocked(route claudeSessionRoute) error {
	store := h.ensureClaudeSessions()
	binding := store.binding(route.BindingKey)
	workspaceRoot := normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
	sessionID := strings.TrimSpace(binding.SessionID)
	mutation, err := h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: route, WorkspaceRoot: workspaceRoot, KeepSelection: true, Command: "cli",
	})
	if err != nil {
		return err
	}
	if mutation.Target.Owner != claudeOwnerLocal || store.controlIntent(sessionID).Owner != claudeOwnerLocal {
		return errClaudeCLILocalHandoffUncertain
	}
	unlockSession, lockErr := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: route.Context, command: "cli open", sessionIDs: []string{sessionID},
	})
	if lockErr != nil {
		return errors.Join(errClaudeCLILocalHandoffUncertain, lockErr)
	}
	lockedIntent := store.controlIntent(sessionID)
	if lockedIntent != mutation.Target || lockedIntent.Owner != claudeOwnerLocal {
		unlockSession()
		return errClaudeCLILocalHandoffUncertain
	}
	if claudeAgent, ok := route.Agent.(interface {
		CurrentClaudeSession(string) (string, bool)
	}); ok {
		conversationID := buildClaudeConversationID(route.UserID, route.AgentName, workspaceRoot)
		if _, bound := claudeAgent.CurrentClaudeSession(conversationID); bound {
			unlockSession()
			return errClaudeCLILocalHandoffUncertain
		}
	}

	request := ClaudeCLIResumeRequest{
		Command: strings.TrimSpace(route.Agent.Info().LocalCommand), WorkspaceRoot: workspaceRoot, SessionID: sessionID,
	}
	openErr := h.resolveClaudeCLIResumeOpener()(route.Context, request)
	unlockSession()
	if openErr == nil {
		return nil
	}
	selected, catalogErr := h.findClaudeSessionForRoute(route, sessionID)
	if catalogErr != nil {
		return errors.Join(errClaudeCLIOpenFailed, errClaudeCLIRemoteRestoreUncertain, openErr, catalogErr)
	}
	if normalizeClaudeWorkspaceRoot(selected.Cwd) != workspaceRoot {
		return errors.Join(
			errClaudeCLIOpenFailed, errClaudeCLIRemoteRestoreUncertain, openErr,
			fmt.Errorf("Claude session 目录与当前 binding 不一致"),
		)
	}
	result, acquireErr := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: selected, Command: "cli compensate",
	})
	if acquireErr != nil {
		return errors.Join(errClaudeCLIOpenFailed, errClaudeCLIRemoteRestoreUncertain, openErr, acquireErr)
	}
	if result.RuntimeErr != nil {
		return errors.Join(errClaudeCLIOpenFailed, errClaudeCLIRemoteRestoreUncertain, openErr, result.RuntimeErr)
	}
	return errors.Join(errClaudeCLIOpenFailed, openErr)
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
