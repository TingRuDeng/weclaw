package messaging

import (
	"fmt"
	"strings"
	"time"
)

// replaceRemoteFirstTurnThread 原子替换已无法 materialize 的空 thread。
// 只有当前 workspace 仍选中旧 thread 且同一路由持续持有 remote owner 时才允许执行。
func (s *codexSessionStore) replaceRemoteFirstTurnThread(
	bindingKey string,
	workspaceRoot string,
	conversationID string,
	oldThreadID string,
	newThreadID string,
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

	currentBinding := s.bindings[bindingKey]
	currentSession, ok := currentBinding.Workspaces[workspaceRoot]
	if !ok || strings.TrimSpace(currentSession.ThreadID) != oldThreadID {
		return errCodexRemoteSelectionChanged
	}
	oldIntent := s.controls[oldThreadID]
	if oldIntent.Owner != codexControlRemote || oldIntent.RouteBindingKey != bindingKey ||
		oldIntent.ConversationID != conversationID {
		return errCodexRemoteSelectionChanged
	}
	if existing, exists := s.controls[newThreadID]; exists && existing.Owner == codexControlRemote &&
		(existing.RouteBindingKey != bindingKey || existing.ConversationID != conversationID) {
		return errCodexRemoteSelectionOtherRoute
	}

	nextBindings := cloneCodexSessionBindings(s.bindings)
	nextControls := cloneCodexControlIntents(s.controls)
	now := time.Now().UTC()
	binding := nextBindings[bindingKey]
	for root, session := range binding.Workspaces {
		if root == workspaceRoot || strings.TrimSpace(session.ThreadID) != newThreadID {
			continue
		}
		session.ThreadID = ""
		session.PendingNewThread = false
		session.PendingFirstTurn = false
		session.UpdatedAt = now.Format(time.RFC3339)
		binding.Workspaces[root] = session
	}
	currentSession.ThreadID = newThreadID
	currentSession.PendingNewThread = false
	currentSession.PendingFirstTurn = true
	currentSession.UpdatedAt = now.Format(time.RFC3339)
	binding.Workspaces[workspaceRoot] = currentSession
	nextBindings[bindingKey] = binding

	nextControls[oldThreadID] = codexControlIntent{
		Owner: codexControlDesktop, Revision: oldIntent.Revision + 1,
		UpdatedAt: now.Format(time.RFC3339Nano),
	}
	oldIntent.UpdatedAt = now.Format(time.RFC3339Nano)
	nextControls[newThreadID] = oldIntent

	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings, Controls: nextControls,
		Updated: now.Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return fmt.Errorf("保存 Codex 首次写入 thread 替换: %w", err)
	}
	s.bindings, s.controls = nextBindings, nextControls
	return nil
}
