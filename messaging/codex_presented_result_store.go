package messaging

import (
	"fmt"
	"strings"
	"time"
)

// claimPresentedResult records the terminal turn before its summary is rendered.
// The in-memory claim remains authoritative for this process if persistence is unavailable.
func (s *codexSessionStore) claimPresentedResult(bindingKey string, threadID string, turnID string) (bool, error) {
	bindingKey = strings.TrimSpace(bindingKey)
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if bindingKey == "" || threadID == "" || turnID == "" {
		return false, nil
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	binding, ok := s.bindings[bindingKey]
	if !ok {
		return false, errCodexRemoteSelectionChanged
	}
	workspaceRoot := normalizeCodexWorkspaceRoot(binding.ActiveWorkspace)
	session := binding.Workspaces[workspaceRoot]
	if session.PendingNewThread || codexWorkspaceReleaseIntent(session) ||
		strings.TrimSpace(session.ThreadID) != threadID {
		return false, errCodexRemoteSelectionChanged
	}
	if strings.TrimSpace(binding.PresentedResultTurns[threadID]) == turnID {
		return false, nil
	}

	nextBindings := cloneCodexSessionBindings(s.bindings)
	binding = nextBindings[bindingKey]
	if binding.PresentedResultTurns == nil {
		binding.PresentedResultTurns = make(map[string]string)
	}
	binding.PresentedResultTurns[threadID] = turnID
	nextBindings[bindingKey] = binding
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Archived: sortedCodexArchivedThreadIDs(s.archived), Updated: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		s.bindings = nextBindings
		return true, fmt.Errorf("保存 Codex 最近任务展示状态: %w", err)
	}
	s.bindings = nextBindings
	return true, nil
}
