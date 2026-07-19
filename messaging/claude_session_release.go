package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

var errClaudeSessionReleaseActive = errors.New("当前 Claude 任务仍在执行")

type claudeSessionReleaseRequest struct {
	Route         claudeSessionRoute
	WorkspaceRoot string
	KeepSelection bool
	Command       string
}

// releaseClaudeSelectionWithBindingLocked changes only the calling frontend's
// binding. It never releases or mutates another frontend bound to the same
// session.
func (h *Handler) releaseClaudeSelectionWithBindingLocked(request claudeSessionReleaseRequest) (claudeBindingMutation, error) {
	route := request.Route
	if strings.TrimSpace(route.BindingKey) == "" {
		return claudeBindingMutation{}, fmt.Errorf("Claude 会话解绑缺少 binding key")
	}
	store := h.ensureClaudeSessions()
	binding := store.binding(route.BindingKey)
	if request.KeepSelection && strings.TrimSpace(binding.SessionID) == "" {
		return claudeBindingMutation{}, fmt.Errorf("当前没有 Claude 会话")
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(request.WorkspaceRoot)
	if request.KeepSelection || workspaceRoot == "" {
		workspaceRoot = normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
	}
	if workspaceRoot == "" {
		workspaceRoot = normalizeClaudeWorkspaceRoot(route.WorkspaceRoot)
	}
	if workspaceRoot == "" {
		return claudeBindingMutation{}, fmt.Errorf("Claude 会话解绑缺少工作空间")
	}

	initial := store.bindingSelectionSnapshot(route.BindingKey, binding.SessionID)
	unlock, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: route.Context, command: request.Command, sessionIDs: []string{binding.SessionID},
	})
	if err != nil {
		return claudeBindingMutation{}, err
	}
	defer unlock()
	locked := store.bindingSelectionSnapshot(route.BindingKey, binding.SessionID)
	if locked != initial {
		return claudeBindingMutation{}, errClaudeBindingSelectionChanged
	}
	if h.hasActiveClaudeBindingSession(route, locked.Binding.SessionID) {
		return claudeBindingMutation{}, errClaudeSessionReleaseActive
	}

	mutation, err := store.commitBindingRelease(claudeBindingReleaseUpdate{
		BindingKey: route.BindingKey, WorkspaceRoot: workspaceRoot,
		KeepSelection: request.KeepSelection, Expected: locked,
	})
	if err != nil {
		return claudeBindingMutation{}, err
	}
	if !request.KeepSelection {
		if claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent); ok && mutation.Previous.SessionID != "" {
			conversationID := buildClaudeConversationID(
				route.UserID, route.AgentName, mutation.Previous.WorkspaceRoot,
			)
			claudeAgent.ClearClaudeSession(conversationID)
		}
	}
	return mutation, nil
}

func (h *Handler) releaseClaudeSelectionForWorkspaceWithBindingLocked(route claudeSessionRoute, workspaceRoot string, command string) (claudeBindingMutation, error) {
	return h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: route, WorkspaceRoot: workspaceRoot, Command: command,
	})
}

// releaseClaudeWorkspaceForUser lets global /cwd move only this frontend to a
// new workspace. Other frontends bound to the same session remain untouched.
// The caller must hold the route binding lock.
func (h *Handler) releaseClaudeWorkspaceForUser(ctx context.Context, userID string, agentName string, ag agent.Agent, workspaceRoot string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	bindingKey := claudeBindingKey(userID, agentName)
	_, err := h.releaseClaudeSelectionForWorkspaceWithBindingLocked(claudeSessionRoute{
		Context: ctx, ActorUserID: userID, UserID: userID, AgentName: agentName,
		Agent: ag, WorkspaceRoot: h.claudeWorkspaceRootForUser(userID, agentName, ag), BindingKey: bindingKey,
	}, workspaceRoot, "cwd")
	return err
}
