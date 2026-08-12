package messaging

import (
	"fmt"
	"strings"
	"time"
)

// replaceRemoteFirstTurnThread 原子替换已无法 materialize 的空 thread。
// 只有当前 workspace 仍选中旧 thread 时才允许执行；不同 frontend 的
// bindings 彼此独立，不参与本次 CAS。
func (s *codexSessionStore) replaceRemoteFirstTurnThread(
	bindingKey string,
	workspaceRoot string,
	conversationID string,
	oldThreadID string,
	newThreadID string,
	recoveryReservationID ...string,
) error {
	bindingKey = strings.TrimSpace(bindingKey)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	conversationID = strings.TrimSpace(conversationID)
	oldThreadID = strings.TrimSpace(oldThreadID)
	newThreadID = strings.TrimSpace(newThreadID)
	if bindingKey == "" || workspaceRoot == "" || conversationID == "" || oldThreadID == "" || newThreadID == "" {
		return fmt.Errorf("替换 Codex 首次写入 thread 缺少必要字段")
	}
	if oldThreadID == newThreadID {
		return nil
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, archived := s.archived[newThreadID]; archived {
		return errCodexRemoteThreadArchived
	}
	currentBinding := s.bindings[bindingKey]
	currentSession, ok := currentBinding.Workspaces[workspaceRoot]
	if !ok || strings.TrimSpace(currentSession.ThreadID) != oldThreadID {
		return errCodexRemoteSelectionChanged
	}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	now := time.Now().UTC()
	binding := nextBindings[bindingKey]
	for root, session := range binding.Workspaces {
		if root == workspaceRoot || strings.TrimSpace(session.ThreadID) != newThreadID {
			continue
		}
		session.ThreadID = ""
		session.PendingNewThread = false
		session.PendingFirstTurn = false
		session.FirstTurnRecoveryThreadID = ""
		session.FirstTurnRecoveryReservationID = ""
		session.UpdatedAt = now.Format(time.RFC3339)
		binding.Workspaces[root] = session
	}
	currentSession.ThreadID = newThreadID
	currentSession.PendingNewThread = false
	currentSession.PendingFirstTurn = true
	currentSession.FirstTurnRecoveryThreadID = oldThreadID
	if len(recoveryReservationID) > 0 {
		currentSession.FirstTurnRecoveryReservationID = strings.TrimSpace(recoveryReservationID[0])
	} else {
		currentSession.FirstTurnRecoveryReservationID = ""
	}
	currentSession.UpdatedAt = now.Format(time.RFC3339)
	binding.Workspaces[workspaceRoot] = currentSession
	if binding.Follower != nil &&
		normalizeCodexWorkspaceRoot(binding.Follower.WorkspaceRoot) == workspaceRoot &&
		strings.TrimSpace(binding.Follower.ThreadID) == oldThreadID {
		binding.Follower = cloneCodexFrontendFollower(binding.Follower)
		binding.Follower.ThreadID = newThreadID
		binding.Follower.UpdatedAt = now.Format(time.RFC3339)
		binding.FollowRevision++
		// 新 thread 已由同一次 turn/start 创建，但此处尚未收到权威 turn ID。
		// 先持久化 pending 空游标，崩溃恢复时 inactive 快照仍会补投该首轮终态。
		binding.FollowTurnID = ""
		binding.FollowTurnInitialized = true
		binding.FollowTurnPending = true
	}
	nextBindings[bindingKey] = binding

	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now.Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return fmt.Errorf("保存 Codex 首次写入 thread 替换: %w", err)
	}
	s.bindings = nextBindings
	return nil
}
