package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

var (
	errClaudeSessionAcquireActiveOld = errors.New("当前 Claude 远程任务仍在执行")
	errClaudeSessionAcquireUncertain = errors.New("Claude 控制权移交结果未确认")
)

type claudeSessionAcquireRequest struct {
	Route    claudeSessionRoute
	Selected agent.ClaudeSession
	Command  string
}

type claudeSessionAcquireResult struct {
	Control        claudeControlIntent
	ConversationID string
	Mutation       claudeRemoteMutation
}

type claudeRuntimeSelectionSnapshot struct {
	ConversationID string
	SessionID      string
	Bound          bool
}

// acquireClaudeSessionWithBindingLocked 在 route binding 外层锁内完成 runtime、持久状态和默认 Agent 的接管事务。
func (h *Handler) acquireClaudeSessionWithBindingLocked(request claudeSessionAcquireRequest) (claudeSessionAcquireResult, error) {
	route := request.Route
	selected := request.Selected
	selected.ID = strings.TrimSpace(selected.ID)
	selected.Cwd = normalizeClaudeWorkspaceRoot(selected.Cwd)
	if strings.TrimSpace(route.BindingKey) == "" || selected.ID == "" || selected.Cwd == "" {
		return claudeSessionAcquireResult{}, fmt.Errorf("Claude 选择接管缺少必要字段")
	}
	claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return claudeSessionAcquireResult{}, fmt.Errorf("当前 Claude Agent 不支持 session 切换")
	}

	store := h.ensureClaudeSessions()
	initial := store.remoteSelectionSnapshot(route.BindingKey, selected.ID)
	initialSessionIDs := claudeRemoteSelectionSessionIDs(initial)
	unlock, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: route.Context, command: request.Command, sessionIDs: initialSessionIDs,
	})
	if err != nil {
		return claudeSessionAcquireResult{}, err
	}
	defer unlock()

	locked := store.remoteSelectionSnapshot(route.BindingKey, selected.ID)
	if !slices.Equal(initialSessionIDs, claudeRemoteSelectionSessionIDs(locked)) {
		return claudeSessionAcquireResult{}, errClaudeRemoteSelectionChanged
	}
	if locked.Target.Owner == claudeOwnerRemote && locked.Target.BindingKey != route.BindingKey {
		return claudeSessionAcquireResult{}, errClaudeRemoteSelectionOtherRoute
	}
	if h.hasActiveReleasedClaudeSession(locked, selected.ID) {
		return claudeSessionAcquireResult{}, errClaudeSessionAcquireActiveOld
	}

	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, selected.Cwd)
	runtimeBefore := captureClaudeRuntimeSelection(claudeAgent, conversationID)
	runtimeChanged, err := h.ensureClaudeRuntimeSelection(route, claudeAgent, locked, runtimeBefore, selected)
	if err != nil {
		// UseClaudeSession 未成功返回时不得根据持久 binding 猜测并重建 runtime。
		return claudeSessionAcquireResult{}, err
	}

	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: route.BindingKey, WorkspaceRoot: selected.Cwd, TargetSessionID: selected.ID,
		ConversationID: conversationID, Expected: locked,
	})
	if err != nil {
		if !runtimeChanged {
			return claudeSessionAcquireResult{}, err
		}
		rollbackErr := h.rollbackClaudeSessionAcquire(route, claudeAgent, runtimeBefore)
		if rollbackErr != nil {
			failClosedErr := h.failClosedClaudeSessionAcquire(store, route.BindingKey, selected.ID, selected.Cwd)
			return claudeSessionAcquireResult{}, errors.Join(errClaudeSessionAcquireUncertain, err, rollbackErr, failClosedErr)
		}
		return claudeSessionAcquireResult{}, err
	}

	if err := h.ensureAgentSessions().Set(route.UserID, route.AgentName); err != nil {
		storeErr := store.rollbackRemoteMutation(mutation)
		var runtimeErr error
		if runtimeChanged {
			runtimeErr = h.rollbackClaudeSessionAcquire(route, claudeAgent, runtimeBefore)
		}
		if storeErr != nil || runtimeErr != nil {
			failClosedErr := h.failClosedClaudeSessionAcquire(store, route.BindingKey, selected.ID, selected.Cwd)
			return claudeSessionAcquireResult{}, errors.Join(errClaudeSessionAcquireUncertain, err, storeErr, runtimeErr, failClosedErr)
		}
		return claudeSessionAcquireResult{}, err
	}

	h.clearReleasedClaudeRuntimeMappings(claudeAgent, mutation, conversationID)
	return claudeSessionAcquireResult{Control: mutation.Target, ConversationID: conversationID, Mutation: mutation}, nil
}

// failClosedClaudeSessionAcquire 在补偿不完整时尽力持久化释放该 route 的全部远程写入权。
func (h *Handler) failClosedClaudeSessionAcquire(store *claudeSessionStore, bindingKey string, targetSessionID string, fallbackWorkspaceRoot string) error {
	snapshot := store.remoteSelectionSnapshot(bindingKey, targetSessionID)
	workspaceRoot := normalizeClaudeWorkspaceRoot(snapshot.Binding.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = normalizeClaudeWorkspaceRoot(fallbackWorkspaceRoot)
	}
	_, err := store.commitRemoteRelease(claudeRemoteReleaseUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspaceRoot, KeepSelection: true, Expected: snapshot,
	})
	if err == nil {
		return nil
	}
	// 持久化介质不可用时，至少让当前进程立即拒绝后续远程写入。
	forceClaudeRemoteFailClosedInMemory(store, bindingKey)
	return err
}

func forceClaudeRemoteFailClosedInMemory(store *claudeSessionStore, bindingKey string) {
	store.saveMu.Lock()
	defer store.saveMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	for sessionID, intent := range store.controls {
		if intent.Owner == claudeOwnerRemote && intent.BindingKey == bindingKey {
			store.controls[sessionID] = claudeControlIntent{Owner: claudeOwnerUnclaimed, Revision: intent.Revision + 1}
		}
	}
}

func claudeRemoteSelectionSessionIDs(snapshot claudeRemoteSelectionSnapshot) []string {
	sessionIDs := make([]string, 0, len(snapshot.RouteOwned)+1)
	sessionIDs = append(sessionIDs, snapshot.TargetSessionID)
	for sessionID := range snapshot.RouteOwned {
		sessionIDs = append(sessionIDs, sessionID)
	}
	return sortedUniqueClaudeSessionIDs(sessionIDs)
}

func (h *Handler) hasActiveReleasedClaudeSession(snapshot claudeRemoteSelectionSnapshot, targetSessionID string) bool {
	for sessionID, intent := range snapshot.RouteOwned {
		if sessionID == targetSessionID {
			continue
		}
		if _, active := h.activeTask(intent.ConversationID); active {
			return true
		}
	}
	return false
}

// ensureClaudeRuntimeSelection 确保目标 conversation 指向所选 session；已持有且 runtime 一致时保持幂等。
func (h *Handler) ensureClaudeRuntimeSelection(
	route claudeSessionRoute,
	claudeAgent agent.ClaudeSessionAgent,
	snapshot claudeRemoteSelectionSnapshot,
	runtimeBefore claudeRuntimeSelectionSnapshot,
	selected agent.ClaudeSession,
) (bool, error) {
	conversationID := runtimeBefore.ConversationID
	h.bindConversationCwd(route.Agent, conversationID, selected.Cwd)
	alreadyOwned := snapshot.Target.Owner == claudeOwnerRemote &&
		snapshot.Target.BindingKey == route.BindingKey &&
		snapshot.Target.ConversationID == conversationID
	if alreadyOwned && runtimeBefore.Bound && runtimeBefore.SessionID == selected.ID {
		return false, nil
	}
	if err := claudeAgent.UseClaudeSession(route.Context, conversationID, selected.ID); err != nil {
		return false, err
	}
	return true, nil
}

func captureClaudeRuntimeSelection(claudeAgent agent.ClaudeSessionAgent, conversationID string) claudeRuntimeSelectionSnapshot {
	sessionID, bound := claudeAgent.CurrentClaudeSession(conversationID)
	sessionID = strings.TrimSpace(sessionID)
	return claudeRuntimeSelectionSnapshot{ConversationID: conversationID, SessionID: sessionID, Bound: bound && sessionID != ""}
}

// rollbackClaudeSessionAcquire 严格恢复接管前从 ACP 读取到的真实 runtime 状态。
func (h *Handler) rollbackClaudeSessionAcquire(route claudeSessionRoute, claudeAgent agent.ClaudeSessionAgent, before claudeRuntimeSelectionSnapshot) error {
	if !before.Bound {
		claudeAgent.ClearClaudeSession(before.ConversationID)
		return nil
	}
	return claudeAgent.UseClaudeSession(route.Context, before.ConversationID, before.SessionID)
}

func (h *Handler) clearReleasedClaudeRuntimeMappings(claudeAgent agent.ClaudeSessionAgent, mutation claudeRemoteMutation, targetConversationID string) {
	cleared := make(map[string]struct{}, len(mutation.Released))
	for sessionID := range mutation.Released {
		previous := mutation.Before.Controls[sessionID]
		conversationID := strings.TrimSpace(previous.ConversationID)
		if conversationID == "" || conversationID == targetConversationID {
			continue
		}
		if _, exists := cleared[conversationID]; exists {
			continue
		}
		claudeAgent.ClearClaudeSession(conversationID)
		cleared[conversationID] = struct{}{}
	}
}

func renderClaudeSessionAcquireFailure(err error) string {
	switch {
	case errors.Is(err, errClaudeRemoteSelectionOtherRoute):
		return "该 Claude 会话正由其他远程窗口控制，请先在原窗口释放控制权。"
	case errors.Is(err, errClaudeSessionAcquireActiveOld):
		return "当前窗口的 Claude 远程任务仍在执行，请等待任务结束或先发送 /stop。"
	case errors.Is(err, errClaudeSessionAcquireUncertain):
		return "Claude 控制权移交结果未确认，已停止继续操作；请检查状态后重试。"
	case errors.Is(err, errClaudeRemoteSelectionChanged), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "Claude 会话状态刚刚发生变化，请重试。"
	default:
		return "切换并接管 Claude 会话失败，请稍后重试。"
	}
}
