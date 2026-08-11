package messaging

import (
	"fmt"
	"strings"
	"time"
)

type codexWorkspaceThreadReleaseResult struct {
	bindingKey  string
	workspace   string
	threadID    string
	before      codexSessionBinding
	after       codexSessionBinding
	beforeExist bool
	changed     bool
}

// releaseWorkspaceThread 持久化解除一个消息窗口当前工作空间的 thread 绑定。
// 它只修改 frontend 路由，不触碰共享 Codex Host 或 thread 本身。
func (s *codexSessionStore) releaseWorkspaceThread(
	bindingKey string,
	workspaceRoot string,
	recoveryReservationID ...string,
) (codexWorkspaceThreadReleaseResult, error) {
	bindingKey = strings.TrimSpace(bindingKey)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if bindingKey == "" || workspaceRoot == "" {
		return codexWorkspaceThreadReleaseResult{}, fmt.Errorf("解除 Codex 会话绑定缺少必要路由字段")
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	before, beforeExist := s.bindings[bindingKey]
	before = cloneCodexSessionBinding(before)
	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding, exists := nextBindings[bindingKey]
	if !exists {
		return codexWorkspaceThreadReleaseResult{bindingKey: bindingKey, workspace: workspaceRoot}, nil
	}
	session, exists := binding.Workspaces[workspaceRoot]
	if !exists || (strings.TrimSpace(session.ThreadID) == "" && !session.PendingNewThread && !session.PendingFirstTurn) {
		return codexWorkspaceThreadReleaseResult{
			bindingKey: bindingKey, workspace: workspaceRoot,
			before: before, after: cloneCodexSessionBinding(binding), beforeExist: beforeExist,
		}, nil
	}
	threadID := strings.TrimSpace(session.ThreadID)
	releasedRecoveryThreadID := strings.TrimSpace(session.FirstTurnRecoveryThreadID)
	releasedRecoveryReservationID := strings.TrimSpace(session.FirstTurnRecoveryReservationID)
	if len(recoveryReservationID) > 0 && strings.TrimSpace(recoveryReservationID[0]) != "" {
		releasedRecoveryReservationID = strings.TrimSpace(recoveryReservationID[0])
	}
	session.ThreadID = ""
	session.PendingNewThread = false
	session.PendingFirstTurn = false
	session.FirstTurnRecoveryThreadID = ""
	session.FirstTurnRecoveryReservationID = ""
	session.ReleasePending = true
	session.Released = false
	session.ReleasedThreadID = threadID
	session.ReleasedRecoveryThreadID = releasedRecoveryThreadID
	session.ReleasedRecoveryReservationID = releasedRecoveryReservationID
	now := time.Now().UTC().Format(time.RFC3339)
	session.UpdatedAt = now
	binding.Workspaces[workspaceRoot] = session
	if binding.Follower != nil &&
		normalizeCodexWorkspaceRoot(binding.Follower.WorkspaceRoot) == workspaceRoot &&
		strings.TrimSpace(binding.Follower.ThreadID) == threadID {
		binding.Follower = nil
		binding.FollowRevision++
	}
	nextBindings[bindingKey] = binding

	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return codexWorkspaceThreadReleaseResult{}, fmt.Errorf("保存 Codex 会话解绑: %w", err)
	}
	s.bindings = nextBindings
	return codexWorkspaceThreadReleaseResult{
		bindingKey: bindingKey, workspace: workspaceRoot, threadID: threadID,
		before: before, after: cloneCodexSessionBinding(binding), beforeExist: beforeExist, changed: true,
	}, nil
}

// commitWorkspaceThreadRelease 仅在任务投递权和活动卡均已冻结后公开解绑墓碑。
// ReleasePending 已先落盘；若本次提交失败，重启加载会继续完成该用户意图。
func (s *codexSessionStore) commitWorkspaceThreadRelease(result codexWorkspaceThreadReleaseResult) error {
	if !result.changed {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameCodexSessionBinding(s.bindings[result.bindingKey], result.after) {
		return errCodexRemoteSelectionChanged
	}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding := nextBindings[result.bindingKey]
	session := binding.Workspaces[result.workspace]
	if !session.ReleasePending || session.Released {
		return errCodexRemoteSelectionChanged
	}
	session.ReleasePending = false
	session.Released = true
	now := time.Now().UTC().Format(time.RFC3339)
	session.UpdatedAt = now
	binding.Workspaces[result.workspace] = session
	nextBindings[result.bindingKey] = binding
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return fmt.Errorf("提交 Codex 会话解绑: %w", err)
	}
	s.bindings = nextBindings
	return nil
}

func (s *codexSessionStore) rollbackWorkspaceThreadRelease(result codexWorkspaceThreadReleaseResult) error {
	if !result.changed {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameCodexSessionBinding(s.bindings[result.bindingKey], result.after) {
		return errCodexRemoteSelectionChanged
	}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	if result.beforeExist {
		nextBindings[result.bindingKey] = cloneCodexSessionBinding(result.before)
	} else {
		delete(nextBindings, result.bindingKey)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return fmt.Errorf("回滚 Codex 会话解绑: %w", err)
	}
	s.bindings = nextBindings
	return nil
}
