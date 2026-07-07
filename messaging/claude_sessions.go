package messaging

import (
	"os"
	"path/filepath"
	"strings"
)

type claudeSessionStore struct {
	store *codexSessionStore
}

func newClaudeSessionStore() *claudeSessionStore {
	return &claudeSessionStore{store: newCodexSessionStore()}
}

// DefaultClaudeSessionFile 返回 Claude workspace/session 列表的默认持久化路径。
func DefaultClaudeSessionFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".weclaw", "claude-sessions.json")
}

func (s *claudeSessionStore) SetFilePath(filePath string) {
	s.store.SetFilePath(filePath)
}

func claudeBindingKey(userID string, agentName string) string {
	return normalizeConversationUserKey(userID) + "\x00" + strings.TrimSpace(agentName)
}

func buildClaudeConversationID(userID string, agentName string, workspaceRoot string) string {
	workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
	return strings.Join([]string{"claude", normalizeConversationUserKey(userID), strings.TrimSpace(agentName), workspaceRoot}, "\x00")
}

func normalizeClaudeWorkspaceRoot(workspaceRoot string) string {
	return normalizeCodexWorkspaceRoot(workspaceRoot)
}

func (s *claudeSessionStore) getSession(bindingKey string, workspaceRoot string) (string, bool) {
	return s.store.getThread(bindingKey, workspaceRoot)
}

func (s *claudeSessionStore) getActiveWorkspace(bindingKey string) (string, bool) {
	return s.store.getActiveWorkspace(bindingKey)
}

func (s *claudeSessionStore) setActiveWorkspace(bindingKey string, workspaceRoot string) {
	s.store.setActiveWorkspace(bindingKey, workspaceRoot)
}

func (s *claudeSessionStore) setSession(bindingKey string, workspaceRoot string, sessionID string) {
	s.store.setThread(bindingKey, workspaceRoot, sessionID)
}

func (s *claudeSessionStore) setPendingNew(bindingKey string, workspaceRoot string) {
	s.store.setPendingNew(bindingKey, workspaceRoot)
}

func (s *claudeSessionStore) ensureWorkspace(bindingKey string, workspaceRoot string) {
	s.store.ensureWorkspace(bindingKey, workspaceRoot)
}

func (s *claudeSessionStore) listWorkspaces(bindingKey string) []codexWorkspaceView {
	return s.store.listWorkspaces(bindingKey)
}

func (s *claudeSessionStore) clearStaleWorkspaceSessions(bindingKey string, visibleByWorkspace map[string]map[string]bool) {
	for workspaceRoot, visibleSessionIDs := range visibleByWorkspace {
		s.store.clearStaleWorkspaceThread(bindingKey, workspaceRoot, visibleSessionIDs)
	}
}

func (s *claudeSessionStore) findWorkspaceBySession(bindingKey string, sessionID string) (string, bool) {
	return s.store.findWorkspaceByThread(bindingKey, sessionID)
}
