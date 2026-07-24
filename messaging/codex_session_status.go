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
		return "", nil, fmt.Errorf("当前没有配置 codex agent")
	}
	ag, err := h.getAgent(ctx, agentName)
	if err != nil {
		return "", nil, fmt.Errorf("codex agent 不可用: %v", err)
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

// renderCodexStatusForRoute 显示 route session 的 thread 状态，同时用真实用户工作空间解释路径。
func (h *Handler) renderCodexStatusForRoute(actorUserID string, routeUserID string, agentName string, ag agent.Agent) string {
	workspaceRoot := h.codexWorkspaceRootForRoute(actorUserID, routeUserID, agentName, ag)
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = h.codexWorkspaceRoot(agentName)
	}
	h.syncCodexThreadFromAgent(routeUserID, agentName, workspaceRoot, ag)

	bindingKey := codexBindingKey(routeUserID, agentName)
	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspaceRoot)
	sessionLabel := h.codexSessionLabelForStatus(bindingKey, workspaceRoot, threadID, pending)
	return wechatCommandText(
		"Codex 状态:",
		"工作空间: "+shortCodexWorkspaceName(workspaceRoot),
		"会话: "+sessionLabel,
	)
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
		"/cx whoami 查看当前 workspace/thread 绑定",
		"/cx ls 查看工作空间或当前工作空间会话",
		"/cx <编号|..> 选择当前列表项或返回上一级",
		"/cx cd <编号|工作空间名|..> 进入工作空间；唯一会话时自动绑定；.. 返回工作空间列表",
		"/cx switch <编号> 切换并绑定当前工作空间会话",
		"/cx new 新建并绑定当前工作空间会话",
		"/cx pwd 查看当前工作空间",
		"/cx status 查看当前工作空间、会话、任务、账号和运行状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx account 查看当前账号；管理员私聊可列出账号并确认切换",
		"/cx account status 查看脱敏账号、受管 Host、最近切换和额度摘要",
		"/cx account use <ID或标签> 管理员私聊切换主机级账号",
		"/cx clean 清理已不存在的 WeClaw 工作空间记录",
		"/cx model status 查看新建 Codex 会话的默认模型配置",
		"/cx model ls 查看可用 Codex 模型",
		"/fast 切换当前 Codex 会话或新会话默认速度",
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
