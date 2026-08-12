package messaging

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type codexRemoteThreadReleaseResult struct {
	bindingKey  string
	threadID    string
	before      codexSessionBinding
	after       codexSessionBinding
	beforeExist bool
	changed     bool
}

// releaseRemoteThread 先持久化候选状态，再解除一个 frontend 内对目标 thread 的全部引用。
func (s *codexSessionStore) releaseRemoteThread(
	bindingKey string,
	threadID string,
) (codexRemoteThreadReleaseResult, error) {
	bindingKey = strings.TrimSpace(bindingKey)
	threadID = strings.TrimSpace(threadID)
	if bindingKey == "" || threadID == "" {
		return codexRemoteThreadReleaseResult{}, fmt.Errorf("Codex 归档解绑缺少必要路由字段")
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	before, beforeExist := s.bindings[bindingKey]
	before = cloneCodexSessionBinding(before)
	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding := nextBindings[bindingKey]
	now := time.Now().UTC().Format(time.RFC3339)
	changed := false
	for workspaceRoot, session := range binding.Workspaces {
		if strings.TrimSpace(session.ThreadID) != threadID {
			continue
		}
		session.ThreadID = ""
		session.PendingNewThread = false
		session.PendingFirstTurn = false
		session.FirstTurnRecoveryThreadID = ""
		session.FirstTurnRecoveryReservationID = ""
		session.UpdatedAt = now
		binding.Workspaces[workspaceRoot] = session
		changed = true
	}
	if binding.Follower != nil && strings.TrimSpace(binding.Follower.ThreadID) == threadID {
		binding.Follower = nil
		binding.FollowRevision++
		clearCodexFollowerTurnState(&binding)
		changed = true
	}
	nextBindings[bindingKey] = binding
	result := codexRemoteThreadReleaseResult{
		bindingKey: bindingKey, threadID: threadID,
		before: before, after: cloneCodexSessionBinding(binding),
		beforeExist: beforeExist, changed: changed,
	}
	if !changed {
		return result, nil
	}
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return codexRemoteThreadReleaseResult{}, fmt.Errorf("保存 Codex 归档解绑: %w", err)
	}
	s.bindings = nextBindings
	return result, nil
}

// rollbackRemoteThreadRelease 只比较并恢复调用方 frontend，保留其他 frontend 的并发变更。
func (s *codexSessionStore) rollbackRemoteThreadRelease(result codexRemoteThreadReleaseResult) error {
	if !result.changed {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, archived := s.archived[result.threadID]; archived {
		return errCodexRemoteThreadArchived
	}
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
		return fmt.Errorf("回滚 Codex 归档解绑: %w", err)
	}
	s.bindings = nextBindings
	return nil
}

// remoteThreadBindingKeys 返回所有仍引用目标 thread 的 frontend binding key。
func (s *codexSessionStore) remoteThreadBindingKeys(threadID string) []string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0)
	for bindingKey, binding := range s.bindings {
		for _, session := range binding.Workspaces {
			if strings.TrimSpace(session.ThreadID) == threadID {
				keys = append(keys, bindingKey)
				break
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// markRemoteThreadArchived 清除所有 frontend 的目标绑定，并持久化墓碑以阻止重启后重新写入。
// 已确认的归档事件即使遇到写盘失败也必须先更新内存，避免当前进程继续使用旧 thread。
func (s *codexSessionStore) markRemoteThreadArchived(threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("Codex 归档墓碑缺少 thread id")
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	nextBindings := cloneCodexSessionBindings(s.bindings)
	nextArchived := cloneCodexArchivedThreadSet(s.archived)
	now := time.Now().UTC().Format(time.RFC3339)
	for bindingKey, binding := range nextBindings {
		for workspaceRoot, session := range binding.Workspaces {
			if strings.TrimSpace(session.ThreadID) != threadID {
				continue
			}
			session.ThreadID = ""
			session.PendingNewThread = false
			session.PendingFirstTurn = false
			session.FirstTurnRecoveryThreadID = ""
			session.FirstTurnRecoveryReservationID = ""
			session.UpdatedAt = now
			binding.Workspaces[workspaceRoot] = session
		}
		if binding.Follower != nil && strings.TrimSpace(binding.Follower.ThreadID) == threadID {
			binding.Follower = nil
			binding.FollowRevision++
			clearCodexFollowerTurnState(&binding)
		}
		nextBindings[bindingKey] = binding
	}
	nextArchived[threadID] = struct{}{}
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(nextArchived), Updated: now,
	}
	err := s.persistCandidate(s.filePath, state)
	s.bindings = nextBindings
	s.archived = nextArchived
	if err != nil {
		return fmt.Errorf("保存 Codex 归档墓碑: %w", err)
	}
	return nil
}

// reconcileVisibleRemoteThreads 以当前 Codex 可见目录为准解除误判或已恢复会话的墓碑。
func (s *codexSessionStore) reconcileVisibleRemoteThreads(visible map[string]bool) error {
	if len(visible) == 0 {
		return nil
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	nextArchived := cloneCodexArchivedThreadSet(s.archived)
	changed := false
	for threadID := range visible {
		threadID = strings.TrimSpace(threadID)
		if _, exists := nextArchived[threadID]; exists {
			delete(nextArchived, threadID)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: cloneCodexSessionBindings(s.bindings),
		Archived: sortedCodexArchivedThreadIDs(nextArchived), Updated: now,
	}); err != nil {
		return fmt.Errorf("保存 Codex 可见会话状态: %w", err)
	}
	s.archived = nextArchived
	return nil
}

func cloneCodexSessionBinding(binding codexSessionBinding) codexSessionBinding {
	return cloneCodexSessionBindings(map[string]codexSessionBinding{"binding": binding})["binding"]
}

func cloneCodexArchivedThreadSet(source map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(source)+1)
	for threadID := range source {
		if threadID = strings.TrimSpace(threadID); threadID != "" {
			cloned[threadID] = struct{}{}
		}
	}
	return cloned
}

func sortedCodexArchivedThreadIDs(source map[string]struct{}) []string {
	threadIDs := make([]string, 0, len(source))
	for threadID := range source {
		if threadID = strings.TrimSpace(threadID); threadID != "" {
			threadIDs = append(threadIDs, threadID)
		}
	}
	sort.Strings(threadIDs)
	return threadIDs
}
