package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// handleCwd 处理内部或测试调用的工作目录切换命令。
func (h *Handler) handleCwd(trimmed string, userID ...string) string {
	return h.handleCwdWithAccess(trimmed, userID, false)
}

// handleCwdForMessage 按消息身份判断 /cwd 权限：管理员可切任意本机目录，普通用户受白名单限制。
func (h *Handler) handleCwdForMessage(trimmed string, msg platform.IncomingMessage) string {
	return h.handleCwdWithAccess(trimmed, []string{msg.UserID}, h.isAdminMessage(msg))
}

func (h *Handler) handleCwdWithAccess(trimmed string, userID []string, admin bool) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		return h.currentCwdStatus()
	}
	absPath, err := resolveCwdPath(arg)
	if err != nil {
		return err.Error()
	}
	if !admin && !h.isWorkspaceAllowed(absPath) {
		log.Printf("[handler] rejected /cwd outside allowed workspace roots: %s", absPath)
		return fmt.Sprintf("该目录不在允许的工作目录范围内：%s\n请联系管理员在 allowed_workspace_roots 中添加。", absPath)
	}
	agents := h.snapshotAgents()
	release := h.lockCwdBindings(userID, agents)
	defer release()
	if h.hasActiveClaudeTaskForCwd(userID, agents) {
		return "当前 Claude 任务正在运行，请等待任务结束或先发送 /stop。"
	}
	if err := h.releaseClaudeWorkspacesForCwd(userID, agents, absPath); err != nil {
		return fmt.Sprintf("切换 Claude 工作空间失败: %v", err)
	}
	h.updateAgentWorkingDirectories(absPath, agents)
	h.recordActiveWorkspaceForUser(userID, agents, absPath)
	return fmt.Sprintf("cwd: %s", absPath)
}

func (h *Handler) releaseClaudeWorkspacesForCwd(userIDs []string, agents map[string]agent.Agent, workspaceRoot string) error {
	if len(userIDs) == 0 || strings.TrimSpace(userIDs[0]) == "" {
		return nil
	}
	names := make([]string, 0, len(agents))
	for name, ag := range agents {
		if isClaudeAgent(name, ag.Info()) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := h.releaseClaudeWorkspaceForUser(context.Background(), userIDs[0], name, agents[name], workspaceRoot); err != nil {
			return err
		}
	}
	return nil
}

// currentCwdStatus 返回默认 Agent 的工作目录提示。
func (h *Handler) currentCwdStatus() string {
	ag := h.getDefaultAgent()
	if ag == nil {
		return "No agent running."
	}
	return wechatCommandText("cwd: (check agent config)", "agent: "+ag.Info().Name)
}

// resolveCwdPath 展开用户目录并校验目标确实是目录。
func resolveCwdPath(arg string) (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		if arg == "~" {
			arg = home
		} else if strings.HasPrefix(arg, "~/") {
			arg = filepath.Join(home, arg[2:])
		}
	}
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("Invalid path: %v", err)
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("Path not found: %s", absPath)
	}
	realPath = filepath.Clean(realPath)
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("Path not found: %s", realPath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Not a directory: %s", realPath)
	}
	return realPath, nil
}

// snapshotAgents 复制 Agent 映射，避免后续流程持有 Handler 主锁。
func (h *Handler) snapshotAgents() map[string]agent.Agent {
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()
	return agents
}

// updateAgentWorkingDirectories 更新运行时和 Handler 中记录的 Agent 工作目录。
func (h *Handler) updateAgentWorkingDirectories(absPath string, agents map[string]agent.Agent) {
	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	if h.agentWorkDirs == nil {
		h.agentWorkDirs = make(map[string]string)
	}
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()
}

// lockCwdBindings 按固定顺序锁定当前用户的所有会话绑定，避免工作空间与任务快照交错。
func (h *Handler) lockCwdBindings(userIDs []string, agents map[string]agent.Agent) func() {
	if len(userIDs) == 0 || strings.TrimSpace(userIDs[0]) == "" {
		return func() {}
	}
	keys := make([]string, 0)
	for name, ag := range agents {
		if isCodexAgent(name, ag.Info()) {
			keys = append(keys, codexBindingExecutionKey(codexBindingKey(userIDs[0], name)))
		}
		if isClaudeAgent(name, ag.Info()) {
			keys = append(keys, claudeBindingExecutionKey(claudeBindingKey(userIDs[0], name)))
		}
	}
	sort.Strings(keys)
	unlocks := make([]func(), 0, len(keys))
	for _, key := range keys {
		unlocks = append(unlocks, h.lockAgentExecution(key))
	}
	return func() {
		for index := len(unlocks) - 1; index >= 0; index-- {
			unlocks[index]()
		}
	}
}

// hasActiveClaudeTaskForCwd 判断工作目录切换是否会覆盖正在执行的 Claude 绑定。
func (h *Handler) hasActiveClaudeTaskForCwd(userIDs []string, agents map[string]agent.Agent) bool {
	if len(userIDs) == 0 || strings.TrimSpace(userIDs[0]) == "" {
		return false
	}
	for name, ag := range agents {
		if !isClaudeAgent(name, ag.Info()) {
			continue
		}
		workspace := h.claudeWorkspaceRootForUser(userIDs[0], name, ag)
		key := buildClaudeConversationID(userIDs[0], name, workspace)
		if _, active := h.activeTask(key); active {
			return true
		}
	}
	return false
}

func (h *Handler) recordActiveWorkspaceForUser(userIDs []string, agents map[string]agent.Agent, workspaceRoot string) {
	if len(userIDs) == 0 || strings.TrimSpace(userIDs[0]) == "" {
		return
	}
	for name, ag := range agents {
		if isCodexAgent(name, ag.Info()) {
			h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(userIDs[0], name), workspaceRoot)
		}
	}
}
