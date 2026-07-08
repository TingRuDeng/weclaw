package messaging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed string, userID ...string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// 没有传路径时，只展示默认 agent 的 cwd 提示。
		ag := h.getDefaultAgent()
		if ag == nil {
			return "No agent running."
		}
		info := ag.Info()
		return wechatCommandText("cwd: (check agent config)", "agent: "+info.Name)
	}

	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// 安全限制：/cwd 只能切到白名单根目录及其子目录，未配置白名单时拒绝远程切换，
	// 防止被授权用户把具备 shell 权限的 agent 指向任意路径。
	if !h.isWorkspaceAllowed(absPath) {
		log.Printf("[handler] rejected /cwd outside allowed workspace roots: %s", absPath)
		return fmt.Sprintf("该目录不在允许的工作目录范围内：%s\n请联系管理员在 allowed_workspace_roots 中添加。", absPath)
	}

	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

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
	h.recordActiveWorkspaceForUser(userID, agents, absPath)

	return fmt.Sprintf("cwd: %s", absPath)
}

func (h *Handler) recordActiveWorkspaceForUser(userIDs []string, agents map[string]agent.Agent, workspaceRoot string) {
	if len(userIDs) == 0 || strings.TrimSpace(userIDs[0]) == "" {
		return
	}
	for name, ag := range agents {
		if isCodexAgent(name, ag.Info()) {
			h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(userIDs[0], name), workspaceRoot)
		}
		if isClaudeAgent(name, ag.Info()) {
			h.ensureClaudeSessions().setActiveWorkspace(claudeBindingKey(userIDs[0], name), workspaceRoot)
		}
	}
}
