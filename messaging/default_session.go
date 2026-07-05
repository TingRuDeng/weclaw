package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// switchDefault switches the default agent. Starts it on demand if needed.
// The change is persisted to config file.
func (h *Handler) switchDefault(ctx context.Context, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("Failed to switch to %q: %v", name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	h.defaultName = name
	h.agents[name] = ag
	h.mu.Unlock()

	// Persist to config file
	if h.saveDefault != nil {
		if err := h.saveDefault(name); err != nil {
			log.Printf("[handler] failed to save default agent to config: %v", err)
		} else {
			log.Printf("[handler] saved default agent %q to config", name)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched default agent: %s -> %s (%s)", old, name, info)
	return fmt.Sprintf("switch to %s", name)
}

// resetDefaultSession resets the session for the given userID on the default agent.
func (h *Handler) resetDefaultSession(ctx context.Context, userID string) string {
	return h.resetDefaultSessionForRoute(ctx, userID, userID)
}

// resetDefaultSessionForRoute 重置 routeUserID 对应会话，避免飞书 thread 的 /new 重置到真实用户全局会话。
func (h *Handler) resetDefaultSessionForRoute(ctx context.Context, actorUserID string, routeUserID string) string {
	if strings.TrimSpace(routeUserID) == "" {
		routeUserID = actorUserID
	}
	name, ag := h.getDefaultAgentWithName()
	if ag == nil {
		return "No agent running."
	}
	if isCodexAgent(name, ag.Info()) {
		return h.resetDefaultCodexSessionForRoute(ctx, actorUserID, routeUserID, name, ag)
	}
	if isClaudeAgent(name, ag.Info()) {
		return h.resetDefaultClaudeSession(ctx, routeUserID, name, ag)
	}
	sessionID, err := ag.ResetSession(ctx, routeUserID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", routeUserID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

func (h *Handler) getDefaultAgentWithName() (string, agent.Agent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return "", nil
	}
	return h.defaultName, h.agents[h.defaultName]
}

// resetDefaultCodexSession 重置当前微信用户正在使用的 Codex 工作空间会话。
func (h *Handler) resetDefaultCodexSession(ctx context.Context, userID string, name string, ag agent.Agent) string {
	return h.resetDefaultCodexSessionForRoute(ctx, userID, userID, name, ag)
}

// resetDefaultCodexSessionForRoute 用真实用户解析工作空间，用 route 会话创建新的 Codex thread。
func (h *Handler) resetDefaultCodexSessionForRoute(ctx context.Context, actorUserID string, routeUserID string, name string, ag agent.Agent) string {
	workspaceRoot := h.codexWorkspaceRootForUser(actorUserID, name, ag)
	conversationID := buildCodexConversationID(routeUserID, name, workspaceRoot)
	h.bindConversationCwd(ag, conversationID, workspaceRoot)
	sessionID, err := ag.ResetSession(ctx, conversationID)
	if err != nil {
		log.Printf("[handler] reset codex session failed for %s: %v", conversationID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	h.recordResetCodexThread(routeUserID, name, workspaceRoot, sessionID)
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// recordResetCodexThread 同步 /new 后的新 thread，避免下一条消息恢复旧工作空间 thread。
func (h *Handler) recordResetCodexThread(userID string, agentName string, workspaceRoot string, threadID string) {
	bindingKey := codexBindingKey(userID, agentName)
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	if strings.TrimSpace(threadID) == "" {
		h.ensureCodexSessions().setPendingNew(bindingKey, workspaceRoot)
		return
	}
	h.ensureCodexSessions().setThread(bindingKey, workspaceRoot, threadID)
}

func (h *Handler) resetDefaultClaudeSession(ctx context.Context, userID string, name string, ag agent.Agent) string {
	workspaceRoot := h.claudeWorkspaceRootForUser(userID, name, ag)
	conversationID := buildClaudeConversationID(userID, name, workspaceRoot)
	sessionID, err := ag.ResetSession(ctx, conversationID)
	if err != nil {
		log.Printf("[handler] reset claude session failed for %s: %v", conversationID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	h.recordResetClaudeSession(userID, name, workspaceRoot, sessionID)
	if sessionID != "" {
		return wechatCommandText(fmt.Sprintf("已创建新的%s会话", name), sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

func (h *Handler) recordResetClaudeSession(userID string, agentName string, workspaceRoot string, sessionID string) {
	bindingKey := claudeBindingKey(userID, agentName)
	h.ensureClaudeSessions().setActiveWorkspace(bindingKey, workspaceRoot)
	if strings.TrimSpace(sessionID) == "" {
		h.ensureClaudeSessions().setPendingNew(bindingKey, workspaceRoot)
		return
	}
	h.ensureClaudeSessions().setSession(bindingKey, workspaceRoot, sessionID)
}
