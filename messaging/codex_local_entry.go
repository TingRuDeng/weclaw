package messaging

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// handleCodexOpenApp 打开当前工作区的 Codex App，并尽量回显当前 thread 便于用户确认。
func (h *Handler) handleCodexOpenApp(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.handleCodexOpenAppForRoute(ctx, userID, userID, agentName, workspaceRoot, ag)
}

// handleCodexOpenAppForRoute 打开真实用户工作空间，并记录 route session 的本地入口状态。
func (h *Handler) handleCodexOpenAppForRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = h.codexWorkspaceRootForRoute(actorUserID, routeUserID, agentName, ag)
	h.syncCodexThreadFromAgent(routeUserID, agentName, workspaceRoot, ag)
	opener := h.resolveCodexAppOpener()
	command := strings.TrimSpace(ag.Info().Command)
	if command == "" {
		return "当前 Codex Agent 未配置 command，无法打开 Codex App。"
	}
	if err := opener(ctx, command, workspaceRoot); err != nil {
		return wechatCommandText(
			fmt.Sprintf("打开 Codex App 失败: %v", err),
			"可发送 /cx cli 使用 Codex CLI 接手当前 thread。",
		)
	}
	bindingKey := codexBindingKey(routeUserID, agentName)
	ownerBindingKey := codexBindingKey(actorUserID, agentName)
	h.setCodexActiveWorkspaceForRoute(bindingKey, ownerBindingKey, workspaceRoot)
	h.setCodexBrowseWorkspace(bindingKey, workspaceRoot)
	h.recordCodexLocalEntry(bindingKey, workspaceRoot, codexLocalEntryApp)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	return wechatCommandText(
		"已打开 Codex App。",
		"工作空间: "+workspaceRoot,
		"thread: "+renderCodexThreadLabel(threadID, pending),
	)
}

func (h *Handler) resolveCodexAppOpener() CodexAppOpener {
	h.mu.RLock()
	opener := h.codexAppOpener
	h.mu.RUnlock()
	if opener == nil {
		return defaultCodexAppOpener
	}
	return opener
}

// defaultCodexAppOpener 使用当前 Codex 命令打开桌面 App 的工作区入口。
func defaultCodexAppOpener(ctx context.Context, command string, workspaceRoot string) error {
	cmd := exec.CommandContext(ctx, command, "app", workspaceRoot)
	cmd.Dir = workspaceRoot
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// handleCodexAttach 将当前 Codex 会话打开到本地可见端；remote-first Agent 使用 resume。
func (h *Handler) handleCodexAttach(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.handleCodexAttachForRoute(ctx, userID, userID, agentName, workspaceRoot, ag)
}

// handleCodexAttachForRoute 让飞书 route session 接手当前可见端或 CLI 恢复流程。
func (h *Handler) handleCodexAttachForRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	visibleAg, ok := ag.(agent.VisibleCompanionAgent)
	if !ok {
		return h.handleCodexAttachResumeForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag)
	}
	if err := visibleAg.OpenVisibleCompanion(ctx); err != nil {
		return fmt.Sprintf("打开 Codex 本地可见端失败: %v", err)
	}
	return "已打开 Codex 本地可见端。"
}

// handleCodexCLI 将当前微信 Codex thread 恢复到本地 CLI，便于电脑端接手。
func (h *Handler) handleCodexCLI(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.handleCodexCLIForRoute(ctx, userID, userID, agentName, workspaceRoot, ag)
}

// handleCodexCLIForRoute 使用 route session 查 thread，用真实用户工作空间启动 CLI。
func (h *Handler) handleCodexCLIForRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.openCodexThreadInCLIForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag, codexCLIOpenText{
		unsupported:      "当前 Codex Agent 不支持 cli。",
		missingCommand:   "当前 Codex Agent 未配置 command，无法打开 Codex CLI。",
		openFailedPrefix: "打开 Codex CLI 失败",
		successTitle:     "已打开 Codex CLI。",
	})
}

func (h *Handler) handleCodexAttachResume(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.handleCodexAttachResumeForRoute(ctx, userID, userID, agentName, workspaceRoot, ag)
}

// handleCodexAttachResumeForRoute 复用 CLI 恢复路径，但保持飞书 route session 不丢失。
func (h *Handler) handleCodexAttachResumeForRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.openCodexThreadInCLIForRoute(ctx, actorUserID, routeUserID, agentName, workspaceRoot, ag, codexCLIOpenText{
		unsupported:      "当前 Codex Agent 不支持 attach。",
		missingCommand:   "当前 Codex Agent 未配置 command，无法打开本地可见端。",
		openFailedPrefix: "打开 Codex 本地可见端失败",
		successTitle:     "已打开 Codex 本地可见端。",
	})
}

type codexCLIOpenText struct {
	unsupported      string
	missingCommand   string
	openFailedPrefix string
	successTitle     string
}

type codexLocalEntryState struct {
	CLIOpened bool
	AppOpened bool
}

const (
	codexLocalEntryCLI = "cli"
	codexLocalEntryApp = "app"
)

func (h *Handler) openCodexThreadInCLI(ctx context.Context, userID string, agentName string, workspaceRoot string, ag agent.Agent, text codexCLIOpenText) string {
	return h.openCodexThreadInCLIForRoute(ctx, userID, userID, agentName, workspaceRoot, ag, text)
}

// openCodexThreadInCLIForRoute 从 route session 读取 thread，再在真实用户工作空间中打开本地 CLI。
func (h *Handler) openCodexThreadInCLIForRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent, text codexCLIOpenText) string {
	if _, ok := ag.(agent.CodexThreadAgent); !ok {
		return text.unsupported
	}
	workspaceRoot = h.codexWorkspaceRootForRoute(actorUserID, routeUserID, agentName, ag)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = h.codexWorkspaceRoot(agentName)
	}
	h.syncCodexThreadFromAgent(routeUserID, agentName, workspaceRoot, ag)
	bindingKey := codexBindingKey(routeUserID, agentName)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	if pending || strings.TrimSpace(threadID) == "" {
		return "当前还没有可接手的 Codex thread，请先通过微信发送一条 Codex 任务。"
	}
	command := strings.TrimSpace(ag.Info().Command)
	if command == "" {
		return text.missingCommand
	}
	if err := h.resolveCodexCLIResumeOpener()(ctx, command, workspaceRoot, threadID); err != nil {
		return fmt.Sprintf("%s: %v", text.openFailedPrefix, err)
	}
	h.recordCodexLocalEntry(bindingKey, workspaceRoot, codexLocalEntryCLI)
	return wechatCommandText(
		text.successTitle,
		"工作空间: "+workspaceRoot,
		"thread: "+threadID,
	)
}

func (h *Handler) recordCodexLocalEntry(bindingKey string, workspaceRoot string, entryType string) {
	key := codexLocalEntryKey(bindingKey, workspaceRoot)
	if key == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codexLocalEntries == nil {
		h.codexLocalEntries = make(map[string]codexLocalEntryState)
	}
	state := h.codexLocalEntries[key]
	switch entryType {
	case codexLocalEntryCLI:
		state.CLIOpened = true
	case codexLocalEntryApp:
		state.AppOpened = true
	default:
		return
	}
	h.codexLocalEntries[key] = state
}

func (h *Handler) codexLocalEntry(bindingKey string, workspaceRoot string) codexLocalEntryState {
	key := codexLocalEntryKey(bindingKey, workspaceRoot)
	if key == "" {
		return codexLocalEntryState{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.codexLocalEntries[key]
}

func codexLocalEntryKey(bindingKey string, workspaceRoot string) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if strings.TrimSpace(bindingKey) == "" || workspaceRoot == "" {
		return ""
	}
	return bindingKey + "\x00" + workspaceRoot
}

func (h *Handler) resolveCodexCLIResumeOpener() CodexCLIResumeOpener {
	h.mu.RLock()
	opener := h.codexCLIResumeOpener
	h.mu.RUnlock()
	if opener == nil {
		return defaultCodexCLIResumeOpener
	}
	return opener
}

// defaultCodexCLIResumeOpener 在 Terminal 中恢复当前 Codex thread，便于本地接手。
func defaultCodexCLIResumeOpener(ctx context.Context, command string, workspaceRoot string, threadID string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("当前平台 %s 暂不支持自动打开可见终端", runtime.GOOS)
	}
	parts := []string{
		shellQuoteForTerminal(command),
		"resume",
		shellQuoteForTerminal(threadID),
		"--cd",
		shellQuoteForTerminal(workspaceRoot),
	}
	script := "tell application \"Terminal\" to do script " + appleScriptQuoteForTerminal(strings.Join(parts, " "))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func shellQuoteForTerminal(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func appleScriptQuoteForTerminal(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

// handleCodexDetach 仅断开本地可见 Companion，后台 endpoint 继续服务微信 remote。
func (h *Handler) handleCodexDetach(ag agent.Agent) string {
	visibleAg, ok := ag.(agent.VisibleCompanionAgent)
	if !ok {
		return "当前 Codex Agent 不支持 detach。"
	}
	if !visibleAg.DetachVisibleCompanion() {
		return "当前没有已连接的 Codex 本地可见端。"
	}
	return "已断开 Codex 本地可见端。"
}
