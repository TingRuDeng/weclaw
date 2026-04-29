package messaging

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type codexSessionStore struct {
	mu       sync.Mutex
	filePath string
	bindings map[string]codexSessionBinding
}

type codexSessionState struct {
	Version  int                            `json:"version"`
	Bindings map[string]codexSessionBinding `json:"bindings"`
	Updated  string                         `json:"updated"`
}

type codexSessionBinding struct {
	ActiveWorkspace string
	Workspaces      map[string]codexWorkspaceSession
}

type codexWorkspaceSession struct {
	ThreadID         string
	PendingNewThread bool
	UpdatedAt        string
}

func newCodexSessionStore() *codexSessionStore {
	return &codexSessionStore{
		bindings: make(map[string]codexSessionBinding),
	}
}

// DefaultCodexSessionFile 返回 Codex workspace/thread 列表的默认持久化路径。
func DefaultCodexSessionFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".weclaw", "codex-sessions.json")
}

// SetFilePath 设置持久化文件路径并加载已有状态。
func (s *codexSessionStore) SetFilePath(filePath string) {
	s.mu.Lock()
	s.filePath = strings.TrimSpace(filePath)
	s.mu.Unlock()
	s.load()
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

func (s *codexSessionStore) getActiveWorkspace(bindingKey string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot := normalizeCodexWorkspaceRoot(s.bindings[bindingKey].ActiveWorkspace)
	return workspaceRoot, workspaceRoot != ""
}

func (s *codexSessionStore) setActiveWorkspace(bindingKey string, workspaceRoot string) {
	s.mu.Lock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.ensureBindingLocked(bindingKey)
	binding.ActiveWorkspace = workspaceRoot
	if workspaceRoot != "" {
		if _, ok := binding.Workspaces[workspaceRoot]; !ok {
			binding.Workspaces[workspaceRoot] = codexWorkspaceSession{}
		}
	}
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
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
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.ensureBindingLocked(bindingKey)
	if _, ok := binding.Workspaces[workspaceRoot]; !ok {
		binding.Workspaces[workspaceRoot] = codexWorkspaceSession{}
	}
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
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

func (s *codexSessionStore) ensureBindingLocked(bindingKey string) codexSessionBinding {
	binding := s.bindings[bindingKey]
	if binding.Workspaces == nil {
		binding.Workspaces = make(map[string]codexWorkspaceSession)
	}
	return binding
}

func (s *codexSessionStore) load() {
	s.mu.Lock()
	filePath := s.filePath
	s.mu.Unlock()
	if filePath == "" {
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[codex-session] failed to read %s: %v", filePath, err)
		}
		return
	}

	var state codexSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[codex-session] failed to parse %s: %v", filePath, err)
		return
	}

	bindings := make(map[string]codexSessionBinding, len(state.Bindings))
	for key, binding := range state.Bindings {
		if strings.TrimSpace(key) == "" {
			continue
		}
		normalized := codexSessionBinding{
			ActiveWorkspace: normalizeCodexWorkspaceRoot(binding.ActiveWorkspace),
			Workspaces:      make(map[string]codexWorkspaceSession),
		}
		for workspaceRoot, session := range binding.Workspaces {
			workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
			if workspaceRoot == "" {
				continue
			}
			normalized.Workspaces[workspaceRoot] = session
		}
		bindings[key] = normalized
	}

	s.mu.Lock()
	s.bindings = bindings
	s.mu.Unlock()
}

func (s *codexSessionStore) save() {
	s.mu.Lock()
	filePath := s.filePath
	if filePath == "" {
		s.mu.Unlock()
		return
	}
	state := codexSessionState{
		Version:  1,
		Bindings: make(map[string]codexSessionBinding, len(s.bindings)),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
	for key, binding := range s.bindings {
		workspaces := make(map[string]codexWorkspaceSession, len(binding.Workspaces))
		for workspaceRoot, session := range binding.Workspaces {
			workspaces[workspaceRoot] = session
		}
		state.Bindings[key] = codexSessionBinding{ActiveWorkspace: binding.ActiveWorkspace, Workspaces: workspaces}
	}
	s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		log.Printf("[codex-session] failed to create state dir: %v", err)
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[codex-session] failed to marshal state: %v", err)
		return
	}
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		log.Printf("[codex-session] failed to write %s: %v", tmpFile, err)
		return
	}
	if err := os.Rename(tmpFile, filePath); err != nil {
		log.Printf("[codex-session] failed to move %s into place: %v", filePath, err)
	}
}
