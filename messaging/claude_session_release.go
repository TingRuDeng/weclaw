package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

var errClaudeSessionReleaseActive = errors.New("当前 Claude 远程任务仍在执行")

type claudeSessionReleaseRequest struct {
	Route         claudeSessionRoute
	WorkspaceRoot string
	KeepSelection bool
	Command       string
}

// releaseClaudeSelectionWithBindingLocked 在调用方持有 route binding 锁时原子释放该 route 的远程所有权。
func (h *Handler) releaseClaudeSelectionWithBindingLocked(request claudeSessionReleaseRequest) (claudeRemoteMutation, error) {
	route := request.Route
	if strings.TrimSpace(route.BindingKey) == "" {
		return claudeRemoteMutation{}, fmt.Errorf("Claude 会话释放缺少 binding key")
	}
	store := h.ensureClaudeSessions()
	binding := store.binding(route.BindingKey)
	targetSessionID := strings.TrimSpace(binding.SessionID)
	if request.KeepSelection && targetSessionID == "" {
		return claudeRemoteMutation{}, fmt.Errorf("当前没有 Claude 会话")
	}
	workspaceRoot := normalizeClaudeWorkspaceRoot(request.WorkspaceRoot)
	if request.KeepSelection || workspaceRoot == "" {
		workspaceRoot = normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
	}
	if workspaceRoot == "" {
		workspaceRoot = normalizeClaudeWorkspaceRoot(route.WorkspaceRoot)
	}
	if workspaceRoot == "" {
		return claudeRemoteMutation{}, fmt.Errorf("Claude 会话释放缺少工作空间")
	}

	initial := store.remoteSelectionSnapshot(route.BindingKey, targetSessionID)
	initialSessionIDs := claudeRemoteSelectionSessionIDs(initial)
	unlock, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: route.Context, command: request.Command, sessionIDs: initialSessionIDs,
	})
	if err != nil {
		return claudeRemoteMutation{}, err
	}
	defer unlock()

	locked := store.remoteSelectionSnapshot(route.BindingKey, targetSessionID)
	if !slices.Equal(initialSessionIDs, claudeRemoteSelectionSessionIDs(locked)) {
		return claudeRemoteMutation{}, errClaudeRemoteSelectionChanged
	}
	if request.KeepSelection && locked.Target.Owner == claudeOwnerRemote && locked.Target.BindingKey != route.BindingKey {
		return claudeRemoteMutation{}, errClaudeRemoteSelectionOtherRoute
	}
	activeConversations := make(map[string]struct{}, len(locked.RouteOwned)+1)
	for _, intent := range locked.RouteOwned {
		if conversationID := strings.TrimSpace(intent.ConversationID); conversationID != "" {
			activeConversations[conversationID] = struct{}{}
		}
	}
	if strings.TrimSpace(locked.Binding.SessionID) != "" {
		conversationID := buildClaudeConversationID(route.UserID, route.AgentName, locked.Binding.WorkspaceRoot)
		activeConversations[conversationID] = struct{}{}
	}
	for conversationID := range activeConversations {
		if _, active := h.activeTask(conversationID); active {
			return claudeRemoteMutation{}, errClaudeSessionReleaseActive
		}
	}

	mutation, err := store.commitRemoteRelease(claudeRemoteReleaseUpdate{
		BindingKey: route.BindingKey, WorkspaceRoot: workspaceRoot,
		KeepSelection: request.KeepSelection, Expected: locked,
	})
	if err != nil {
		return claudeRemoteMutation{}, err
	}
	if claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent); ok {
		h.clearReleasedClaudeRuntimeMappings(claudeAgent, mutation, "")
	}
	return mutation, nil
}

func (h *Handler) releaseClaudeSelectionForWorkspaceWithBindingLocked(route claudeSessionRoute, workspaceRoot string, command string) (claudeRemoteMutation, error) {
	return h.releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest{
		Route: route, WorkspaceRoot: workspaceRoot, Command: command,
	})
}

// releaseClaudeWorkspaceForUser 供全局 /cwd 在更新 Agent 工作目录前释放 Claude 远程所有权。
// 调用方必须已持有对应 route binding 锁。
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
