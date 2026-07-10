package messaging

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

func (h *Handler) switchCodexWorkspace(agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	ag.SetCwd(workspaceRoot)

	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	h.agentWorkDirs[agentName] = workspaceRoot
	h.mu.Unlock()
	log.Printf("[handler] switched codex workspace for agent %s: %s", agentName, workspaceRoot)
	return workspaceRoot
}

func (h *Handler) getCodexSessionAgent(ctx context.Context) (string, agent.Agent, error) {
	agentName, ok := h.codexAgentName()
	if !ok {
		return "", nil, fmt.Errorf("当前没有配置 Codex Agent。")
	}
	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return "", nil, fmt.Errorf("Codex Agent 不可用: %v", err)
	}
	return agentName, ag, nil
}

func (h *Handler) codexAgentName() (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ag, ok := h.agents["codex"]; ok && isCodexAgent("codex", ag.Info()) {
		return "codex", true
	}
	if h.defaultName != "" {
		if ag, ok := h.agents[h.defaultName]; ok && isCodexAgent(h.defaultName, ag.Info()) {
			return h.defaultName, true
		}
	}
	for _, meta := range h.agentMetas {
		if meta.Name == "codex" || isCodexAgent(meta.Name, agent.AgentInfo{Name: meta.Name, Type: meta.Type, Command: meta.Command}) {
			return meta.Name, true
		}
	}
	return "", false
}

func (h *Handler) codexWorkspaceRoot(agentName string) string {
	h.mu.RLock()
	workspaceRoot := h.agentWorkDirs[agentName]
	h.mu.RUnlock()
	if workspaceRoot == "" {
		workspaceRoot = defaultAttachmentWorkspace()
	}
	return normalizeCodexWorkspaceRoot(workspaceRoot)
}

func (h *Handler) codexWorkspaceRootForUser(userID string, agentName string, ag agent.Agent) string {
	bindingKey := codexBindingKey(userID, agentName)
	workspaceRoot, ok := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	if !ok {
		return h.codexWorkspaceRoot(agentName)
	}
	return h.applyCodexWorkspaceRoot(agentName, ag, workspaceRoot)
}

// codexWorkspaceRootForRoute 解析 route 自己的工作空间；只有真实用户主会话会同步全局 cwd。
func (h *Handler) codexWorkspaceRootForRoute(ownerUserID string, routeUserID string, agentName string, ag agent.Agent) string {
	routeUserID = strings.TrimSpace(routeUserID)
	if routeUserID == "" {
		routeUserID = ownerUserID
	}
	if workspaceRoot, ok := h.ensureCodexSessions().getActiveWorkspace(codexBindingKey(routeUserID, agentName)); ok {
		return h.applyCodexWorkspaceRootForRoute(ownerUserID, routeUserID, agentName, ag, workspaceRoot)
	}
	return h.codexWorkspaceRootForUser(ownerUserID, agentName, ag)
}

func (h *Handler) applyCodexWorkspaceRoot(agentName string, ag agent.Agent, workspaceRoot string) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	ag.SetCwd(workspaceRoot)
	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	h.agentWorkDirs[agentName] = workspaceRoot
	h.mu.Unlock()
	return workspaceRoot
}

func (h *Handler) applyCodexWorkspaceRootForRoute(ownerUserID string, routeUserID string, agentName string, ag agent.Agent, workspaceRoot string) string {
	if shouldSyncCodexGlobalWorkspace(ownerUserID, routeUserID) {
		return h.applyCodexWorkspaceRoot(agentName, ag, workspaceRoot)
	}
	return normalizeCodexWorkspaceRoot(workspaceRoot)
}

func (h *Handler) switchCodexWorkspaceForRoute(ownerUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	if shouldSyncCodexGlobalWorkspace(ownerUserID, routeUserID) {
		return h.switchCodexWorkspace(agentName, workspaceRoot, ag)
	}
	return normalizeCodexWorkspaceRoot(workspaceRoot)
}

func shouldSyncCodexGlobalWorkspace(ownerUserID string, routeUserID string) bool {
	ownerUserID = strings.TrimSpace(ownerUserID)
	routeUserID = strings.TrimSpace(routeUserID)
	return routeUserID == "" || routeUserID == ownerUserID
}

func (h *Handler) renderCodexWhoami(bindingKey string, workspaceRoot string) string {
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	return wechatCommandText("workspace: "+workspaceRoot, "thread: "+renderCodexThreadLabel(threadID, pending))
}

func (h *Handler) renderCodexStatus(userID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	return h.renderCodexStatusForRoute(userID, userID, agentName, workspaceRoot, ag)
}

// renderCodexStatusForRoute 显示 route session 的 thread 状态，同时用真实用户工作空间解释路径。
func (h *Handler) renderCodexStatusForRoute(actorUserID string, routeUserID string, agentName string, workspaceRoot string, ag agent.Agent) string {
	workspaceRoot = h.codexWorkspaceRootForRoute(actorUserID, routeUserID, agentName, ag)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = h.codexWorkspaceRoot(agentName)
	}
	h.syncCodexThreadFromAgent(routeUserID, agentName, workspaceRoot, ag)

	bindingKey := codexBindingKey(routeUserID, agentName)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	sessionLabel := h.codexSessionLabelForStatus(bindingKey, workspaceRoot, threadID, pending)
	localEntry := h.codexLocalEntry(bindingKey, workspaceRoot)
	return wechatCommandText(
		"Codex 状态:",
		"工作空间: "+workspaceRoot,
		"会话: "+sessionLabel,
		"remote: 已配置 ("+ag.Info().Type+")",
		"本地入口:",
		"CLI: "+renderCodexLocalEntry(localEntry.CLIOpened),
		"App: "+renderCodexLocalEntry(localEntry.AppOpened),
		"说明: 本地入口只记录最近打开动作，不实时检测手动关闭。",
	)
}

func renderCodexLocalEntry(opened bool) string {
	if opened {
		return "已打开过"
	}
	return "未打开过"
}

func (h *Handler) renderCodexList(bindingKey string) string {
	return h.renderCodexListForAccess(bindingKey, "", false)
}

func (h *Handler) renderCodexListForAccess(bindingKey string, actorUserID string, admin bool) string {
	if workspaceRoot, ok := h.codexBrowseWorkspace(bindingKey); ok {
		if !admin && !h.isWorkspaceAllowed(workspaceRoot) {
			h.clearCodexBrowseWorkspace(bindingKey)
			return h.renderCodexWorkspaceListForAccess(bindingKey, actorUserID, admin)
		}
		return h.renderCodexSessionList(bindingKey, workspaceRoot)
	}
	return h.renderCodexWorkspaceListForAccess(bindingKey, actorUserID, admin)
}

func renderCodexThreadLabel(threadID string, pending bool) string {
	if pending {
		return "(new draft)"
	}
	if strings.TrimSpace(threadID) == "" {
		return "(none)"
	}
	return threadID
}

func buildCodexSessionHelpText() string {
	return wechatCommandText(
		"Codex 会话命令:",
		"/cx ls 查看工作空间或当前工作空间会话",
		"/cx <编号|..> 选择当前列表项或返回上一级",
		"/cx cd <编号|工作空间名|..> 进入工作空间或返回工作空间列表",
		"/cx switch <编号> 切换当前工作空间会话",
		"/cx new 新建当前工作空间会话",
		"/cx pwd 查看当前工作空间",
		"/cx cli 打开本地 CLI 接手当前 thread",
		"/cx app 打开 Codex App 到当前工作空间",
		"/cx status 查看 remote、thread 和本地入口状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx clean 清理已不存在的 WeClaw 工作空间记录",
		"/cx detach 断开已连接的本地 Companion",
		"/cx model status 查看 Codex 模型状态",
		"/cx model ls 查看可用 Codex 模型",
	)
}

func isSupportedProgressMode(mode string) bool {
	switch mode {
	case progressModeOff, progressModeTyping, progressModeSummary, progressModeVerbose, progressModeStream, progressModeDebug:
		return true
	default:
		return false
	}
}

func isCodexAgent(name string, info agent.AgentInfo) bool {
	if strings.EqualFold(name, "codex") || strings.EqualFold(info.Name, "codex") {
		return true
	}
	command := strings.ToLower(filepath.Base(info.Command))
	return strings.Contains(command, "codex")
}
