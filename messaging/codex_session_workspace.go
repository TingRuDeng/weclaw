package messaging

import (
	"sort"
	"strings"
	"time"
)

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
	intent := s.controls[threadID]
	if intent.Owner == codexControlRemote && intent.RouteBindingKey == bindingKey {
		// thread/start 创建的会话在首条消息前不会出现在 Codex App 的展示目录中。
		// 展示目录的暂时缺失不能覆盖当前窗口已经持久化的远程所有权。
		s.mu.Unlock()
		return false
	}
	session.ThreadID = ""
	session.PendingNewThread = false
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	binding.Workspaces[workspaceRoot] = session
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
	binding := s.ensureBindingLocked(bindingKey)
	if session.ThreadID != "" {
		// 同一个 Codex thread 只能属于一个 workspace，避免后续切换时按错误 cwd 恢复。
		for root, existing := range binding.Workspaces {
			if root == workspaceRoot || strings.TrimSpace(existing.ThreadID) != session.ThreadID {
				continue
			}
			existing.ThreadID = ""
			existing.PendingNewThread = false
			existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			binding.Workspaces[root] = existing
		}
	}
	binding.Workspaces[workspaceRoot] = session
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
}
