package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

var (
	errClaudeSessionAcquireActiveOld = errors.New("当前 Claude 远程任务仍在执行")
	errClaudeSessionAcquireUncertain = errors.New("Claude 会话绑定结果未确认")
)

type claudeSessionAcquireRequest struct {
	Route    claudeSessionRoute
	Selected agent.ClaudeSession
	Command  string
}

type claudeSessionAcquireResult struct {
	SessionID      string
	Binding        claudeSessionBinding
	ConversationID string
	Mutation       claudeBindingMutation
	RuntimeErr     error
}

type claudeRuntimeSelectionSnapshot struct {
	ConversationID string
	SessionID      string
	Bound          bool
}

// acquireClaudeSessionWithBindingLocked 在 route binding 外层锁内先提交窗口绑定，再同步共享 ClaudeHost runtime。
func (h *Handler) acquireClaudeSessionWithBindingLocked(request claudeSessionAcquireRequest) (claudeSessionAcquireResult, error) {
	route := request.Route
	selected := request.Selected
	selected.ID = strings.TrimSpace(selected.ID)
	selected.Cwd = normalizeClaudeWorkspaceRoot(selected.Cwd)
	if strings.TrimSpace(route.BindingKey) == "" || selected.ID == "" || selected.Cwd == "" {
		return claudeSessionAcquireResult{}, fmt.Errorf("Claude 会话绑定缺少必要字段")
	}
	claudeAgent, ok := route.Agent.(agent.ClaudeSessionAgent)
	if !ok {
		return claudeSessionAcquireResult{}, fmt.Errorf("当前 Claude Agent 不支持 session 切换")
	}

	store := h.ensureClaudeSessions()
	initial := store.bindingSelectionSnapshot(route.BindingKey, selected.ID)
	initialSessionIDs := claudeBindingMutationSessionIDs(initial, selected.ID)
	unlock, err := h.lockClaudeSessionControls(claudeSessionLockRequest{
		ctx: route.Context, command: request.Command, sessionIDs: initialSessionIDs,
	})
	if err != nil {
		return claudeSessionAcquireResult{}, err
	}
	defer unlock()

	locked := store.bindingSelectionSnapshot(route.BindingKey, selected.ID)
	if locked != initial {
		return claudeSessionAcquireResult{}, errClaudeBindingSelectionChanged
	}
	if h.hasActiveClaudeBindingSession(route, locked.Binding.SessionID) && locked.Binding.SessionID != selected.ID {
		return claudeSessionAcquireResult{}, errClaudeSessionAcquireActiveOld
	}

	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, selected.Cwd)
	h.bindConversationCwd(route.Agent, conversationID, selected.Cwd)
	runtimeBefore := captureClaudeRuntimeSelection(claudeAgent, conversationID)
	bindingStatus := claudeBindingResumeFailed
	if runtimeBefore.Bound && runtimeBefore.SessionID == selected.ID {
		bindingStatus = claudeBindingReady
	}

	mutation, err := store.commitBindingSelection(claudeBindingSelectionUpdate{
		BindingKey: route.BindingKey, WorkspaceRoot: selected.Cwd, TargetSessionID: selected.ID,
		BindingStatus: bindingStatus, Expected: locked,
	})
	if err != nil {
		return claudeSessionAcquireResult{}, err
	}

	if err := h.ensureAgentSessions().Set(route.UserID, route.AgentName); err != nil {
		storeErr := store.rollbackBindingMutation(mutation)
		if storeErr != nil {
			failClosedErr := forceClaudeBindingFailClosedInMemory(store, route.BindingKey)
			return claudeSessionAcquireResult{}, errors.Join(errClaudeSessionAcquireUncertain, err, storeErr, failClosedErr)
		}
		return claudeSessionAcquireResult{}, err
	}

	result := claudeSessionAcquireResult{
		SessionID: selected.ID, Binding: mutation.Current,
		ConversationID: conversationID, Mutation: mutation,
	}
	if bindingStatus == claudeBindingReady {
		h.clearPreviousClaudeRuntimeMapping(claudeAgent, route, mutation, conversationID)
		return result, nil
	}
	if _, err := h.ensureClaudeRuntimeSelection(route, claudeAgent, runtimeBefore, selected); err != nil {
		h.clearPreviousClaudeRuntimeMapping(claudeAgent, route, mutation, conversationID)
		result.RuntimeErr = err
		return result, nil
	}
	if err := store.markReady(route.BindingKey); err != nil {
		h.clearPreviousClaudeRuntimeMapping(claudeAgent, route, mutation, conversationID)
		result.RuntimeErr = fmt.Errorf("保存 Claude 运行通道状态: %w", err)
		return result, nil
	}
	h.clearPreviousClaudeRuntimeMapping(claudeAgent, route, mutation, conversationID)
	return result, nil
}

// forceClaudeBindingFailClosedInMemory makes an uncertain binding non-writable
// without changing another frontend or pretending that persistence succeeded.
func forceClaudeBindingFailClosedInMemory(store *claudeSessionStore, bindingKey string) error {
	store.saveMu.Lock()
	defer store.saveMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	binding := store.bindings[strings.TrimSpace(bindingKey)]
	if binding.SessionID == "" {
		return nil
	}
	store.bindings[strings.TrimSpace(bindingKey)] = nextClaudeBinding(
		binding, binding.WorkspaceRoot, binding.SessionID, claudeBindingResumeFailed, time.Now().UTC(),
	)
	return nil
}

func claudeBindingMutationSessionIDs(snapshot claudeBindingSelectionSnapshot, targetSessionID string) []string {
	return sortedUniqueClaudeSessionIDs([]string{snapshot.Binding.SessionID, targetSessionID})
}

// ensureClaudeRuntimeSelection 确保目标 conversation 指向所选 session；已持有且 runtime 一致时保持幂等。
func (h *Handler) ensureClaudeRuntimeSelection(
	route claudeSessionRoute,
	claudeAgent agent.ClaudeSessionAgent,
	runtimeBefore claudeRuntimeSelectionSnapshot,
	selected agent.ClaudeSession,
) (bool, error) {
	conversationID := runtimeBefore.ConversationID
	// session/new 成功后 ACP 已把 conversation runtime 指向新 session；此时只需提交
	// binding，不能再发一次 session/resume。普通 switch 若 runtime 已精确命中目标，
	// 同样可以安全复用现状。
	if runtimeBefore.Bound && runtimeBefore.SessionID == selected.ID {
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

// rollbackClaudeSessionAcquire 严格恢复绑定事务前从 ACP 读取到的真实 runtime 状态。
func (h *Handler) rollbackClaudeSessionAcquire(route claudeSessionRoute, claudeAgent agent.ClaudeSessionAgent, before claudeRuntimeSelectionSnapshot) error {
	if !before.Bound {
		claudeAgent.ClearClaudeSession(before.ConversationID)
		return nil
	}
	return claudeAgent.UseClaudeSession(route.Context, before.ConversationID, before.SessionID)
}

func (h *Handler) clearPreviousClaudeRuntimeMapping(
	claudeAgent agent.ClaudeSessionAgent,
	route claudeSessionRoute,
	mutation claudeBindingMutation,
	targetConversationID string,
) {
	previous := mutation.Previous
	if previous.SessionID == "" || previous.WorkspaceRoot == "" {
		return
	}
	conversationID := buildClaudeConversationID(route.UserID, route.AgentName, previous.WorkspaceRoot)
	if conversationID != targetConversationID {
		claudeAgent.ClearClaudeSession(conversationID)
	}
}

func renderClaudeSessionAcquireFailure(err error) string {
	switch {
	case errors.Is(err, errClaudeSessionAcquireActiveOld):
		return "当前窗口的 Claude 任务仍在执行，请等待任务结束或先发送 /stop。"
	case errors.Is(err, errClaudeSessionAcquireUncertain):
		return "Claude 会话绑定结果未确认，已停止继续操作；请检查状态后重试。"
	case errors.Is(err, errClaudeBindingSelectionChanged), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "Claude 会话绑定刚刚发生变化，请重试。"
	default:
		return "切换 Claude 会话失败，请稍后重试。"
	}
}
