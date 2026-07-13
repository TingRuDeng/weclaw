package messaging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
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

	// 普通用户只能切到白名单根目录及其子目录；管理员已经通过 admin_users 显式授权，
	// 可以绕过 allowed_workspace_roots 处理临时排障和本机维护场景。
	if !admin && !h.isWorkspaceAllowed(absPath) {
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
			if err := h.ensureClaudeSessions().commitWorkspace(claudeBindingKey(userIDs[0], name), workspaceRoot); err != nil {
				log.Printf("[handler] failed to save Claude workspace: %v", err)
			}
		}
	}
}
