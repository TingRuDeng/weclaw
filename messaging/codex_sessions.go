package messaging

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type codexSessionStore struct {
	mu       sync.Mutex
	bindings map[string]codexSessionBinding
}

type codexSessionBinding struct {
	Workspaces map[string]codexWorkspaceSession
}

type codexWorkspaceSession struct {
	ThreadID         string
	PendingNewThread bool
	UpdatedAt        string
}

type codexWorkspaceView struct {
	WorkspaceRoot    string
	ThreadID         string
	PendingNewThread bool
}

func newCodexSessionStore() *codexSessionStore {
	return &codexSessionStore{
		bindings: make(map[string]codexSessionBinding),
	}
}

func codexBindingKey(userID string, agentName string) string {
	return strings.TrimSpace(userID) + "\x00" + strings.TrimSpace(agentName)
}

func buildCodexConversationID(userID string, agentName string, workspaceRoot string) string {
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	return strings.Join([]string{"codex", strings.TrimSpace(userID), strings.TrimSpace(agentName), workspaceRoot}, "\x00")
}

func normalizeCodexWorkspaceRoot(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return workspaceRoot
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		return abs
	}
	return workspaceRoot
}

func (s *codexSessionStore) getThread(bindingKey string, workspaceRoot string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	session := s.bindings[bindingKey].Workspaces[workspaceRoot]
	return session.ThreadID, session.PendingNewThread
}

func (s *codexSessionStore) setThread(bindingKey string, workspaceRoot string, threadID string) {
	s.updateWorkspace(bindingKey, workspaceRoot, codexWorkspaceSession{
		ThreadID:         strings.TrimSpace(threadID),
		PendingNewThread: false,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *codexSessionStore) setPendingNew(bindingKey string, workspaceRoot string) {
	s.updateWorkspace(bindingKey, workspaceRoot, codexWorkspaceSession{
		ThreadID:         "",
		PendingNewThread: true,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *codexSessionStore) ensureWorkspace(bindingKey string, workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.ensureBindingLocked(bindingKey)
	if _, ok := binding.Workspaces[workspaceRoot]; !ok {
		binding.Workspaces[workspaceRoot] = codexWorkspaceSession{}
	}
	s.bindings[bindingKey] = binding
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

func (s *codexSessionStore) updateWorkspace(bindingKey string, workspaceRoot string, session codexWorkspaceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.ensureBindingLocked(bindingKey)
	binding.Workspaces[workspaceRoot] = session
	s.bindings[bindingKey] = binding
}

func (s *codexSessionStore) ensureBindingLocked(bindingKey string) codexSessionBinding {
	binding := s.bindings[bindingKey]
	if binding.Workspaces == nil {
		binding.Workspaces = make(map[string]codexWorkspaceSession)
	}
	return binding
}
