package messaging

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// recordThreadUnlessReleased 原子提交 Agent 迟到回填；显式 release 墓碑一旦先提交，
// 后续映射不得靠普通 setThread 清除它。显式 switch/new 仍走 remote selection 提交。
func (s *codexSessionStore) recordThreadUnlessReleased(bindingKey string, workspaceRoot string, threadID string) (bool, error) {
	bindingKey = strings.TrimSpace(bindingKey)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	threadID = strings.TrimSpace(threadID)
	if bindingKey == "" || workspaceRoot == "" || threadID == "" {
		return false, nil
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, archived := s.archived[threadID]; archived {
		return false, nil
	}
	current := s.bindings[bindingKey].Workspaces[workspaceRoot]
	if codexWorkspaceReleaseIntent(current) {
		return false, nil
	}
	if strings.TrimSpace(current.ThreadID) == threadID &&
		!current.PendingNewThread && !current.PendingFirstTurn {
		return true, nil
	}

	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding := nextBindings[bindingKey]
	if binding.Workspaces == nil {
		binding.Workspaces = make(map[string]codexWorkspaceSession)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for root, existing := range binding.Workspaces {
		if root == workspaceRoot || strings.TrimSpace(existing.ThreadID) != threadID {
			continue
		}
		existing.ThreadID = ""
		existing.PendingNewThread = false
		existing.PendingFirstTurn = false
		existing.FirstTurnRecoveryThreadID = ""
		existing.FirstTurnRecoveryReservationID = ""
		existing.UpdatedAt = now
		binding.Workspaces[root] = existing
	}
	current.ThreadID = threadID
	current.PendingNewThread = false
	current.PendingFirstTurn = false
	current.FirstTurnRecoveryThreadID = ""
	current.FirstTurnRecoveryReservationID = ""
	clearCodexWorkspaceReleaseState(&current)
	current.UpdatedAt = now
	binding.Workspaces[workspaceRoot] = current
	nextBindings[bindingKey] = binding
	if err := s.persistCandidate(s.filePath, codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: now,
	}); err != nil {
		return false, fmt.Errorf("保存 Codex Agent 会话回填: %w", err)
	}
	s.bindings = nextBindings
	return true, nil
}

func (s *codexSessionStore) listWorkspaces(bindingKey string) []codexWorkspaceView {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[bindingKey]
	roots := make([]string, 0, len(binding.Workspaces))
	for root := range binding.Workspaces {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	views := make([]codexWorkspaceView, 0, len(roots))
	for _, root := range roots {
		session := binding.Workspaces[root]
		views = append(views, codexWorkspaceView{
			WorkspaceRoot:    root,
			ThreadID:         session.ThreadID,
			PendingNewThread: session.PendingNewThread,
		})
	}
	return views
}

func (s *codexSessionStore) cleanMissingWorkspaces(bindingKey string) []string {
	s.mu.Lock()
	binding := s.bindings[bindingKey]
	if binding.Workspaces == nil {
		s.mu.Unlock()
		return nil
	}

	removed := make([]string, 0)
	for root := range binding.Workspaces {
		if localCodexWorkspaceExists(root) {
			continue
		}
		delete(binding.Workspaces, root)
		if binding.Follower != nil && normalizeCodexWorkspaceRoot(binding.Follower.WorkspaceRoot) == root {
			binding.Follower = nil
			binding.FollowRevision++
			clearCodexFollowerTurnState(&binding)
		}
		removed = append(removed, root)
	}
	if len(removed) == 0 {
		s.mu.Unlock()
		return nil
	}
	sort.Strings(removed)
	if !localCodexWorkspaceExists(binding.ActiveWorkspace) {
		binding.ActiveWorkspace = ""
	}
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
	return removed
}

func (s *codexSessionStore) clearStaleWorkspaceThread(bindingKey string, workspaceRoot string, visibleThreadIDs map[string]bool) bool {
	s.mu.Lock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.bindings[bindingKey]
	session, ok := binding.Workspaces[workspaceRoot]
	if !ok || session.PendingNewThread {
		s.mu.Unlock()
		return false
	}
	threadID := strings.TrimSpace(session.ThreadID)
	if threadID == "" || visibleThreadIDs[threadID] {
		s.mu.Unlock()
		return false
	}
	if session.PendingFirstTurn {
		// thread/start 创建的会话在首条消息前不会出现在 Codex App 的展示目录中。
		// 展示目录的暂时缺失不能覆盖当前 frontend 已持久化的 binding。
		s.mu.Unlock()
		return false
	}
	session.ThreadID = ""
	session.PendingNewThread = false
	session.PendingFirstTurn = false
	session.FirstTurnRecoveryThreadID = ""
	session.FirstTurnRecoveryReservationID = ""
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	binding.Workspaces[workspaceRoot] = session
	if binding.Follower != nil &&
		normalizeCodexWorkspaceRoot(binding.Follower.WorkspaceRoot) == workspaceRoot &&
		strings.TrimSpace(binding.Follower.ThreadID) == threadID {
		binding.Follower = nil
		binding.FollowRevision++
		clearCodexFollowerTurnState(&binding)
	}
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
	return true
}

func (s *codexSessionStore) findWorkspaceByThread(bindingKey string, threadID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", false
	}

	binding := s.bindings[bindingKey]
	var matchedRoot string
	var matchedUpdatedAt string
	for root, session := range binding.Workspaces {
		if strings.TrimSpace(session.ThreadID) != threadID {
			continue
		}
		if matchedRoot == "" || session.UpdatedAt > matchedUpdatedAt {
			matchedRoot = root
			matchedUpdatedAt = session.UpdatedAt
		}
	}
	return matchedRoot, matchedRoot != ""
}

func (s *codexSessionStore) updateWorkspace(bindingKey string, workspaceRoot string, session codexWorkspaceSession) {
	s.mu.Lock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	session.ThreadID = strings.TrimSpace(session.ThreadID)
	// 归档墓碑必须压过晚到的 Agent 同步，不能把已归档 thread 写回持久化 binding。
	if _, archived := s.archived[session.ThreadID]; session.ThreadID != "" && archived {
		s.mu.Unlock()
		return
	}
	binding := s.ensureBindingLocked(bindingKey)
	if session.ThreadID != "" {
		// 同一个 Codex thread 只能属于一个 workspace，避免后续切换时按错误 cwd 恢复。
		for root, existing := range binding.Workspaces {
			if root == workspaceRoot || strings.TrimSpace(existing.ThreadID) != session.ThreadID {
				continue
			}
			existing.ThreadID = ""
			existing.PendingNewThread = false
			existing.PendingFirstTurn = false
			existing.FirstTurnRecoveryThreadID = ""
			existing.FirstTurnRecoveryReservationID = ""
			existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			binding.Workspaces[root] = existing
		}
	}
	binding.Workspaces[workspaceRoot] = session
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
}
