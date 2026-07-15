package messaging

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type codexSessionStateWriter func(filePath string, data []byte) error

type codexSessionStore struct {
	mu         sync.Mutex
	saveMu     sync.Mutex
	filePath   string
	bindings   map[string]codexSessionBinding
	controls   map[string]codexControlIntent
	writeState codexSessionStateWriter
}

type codexSessionState struct {
	Version  int                            `json:"version"`
	Bindings map[string]codexSessionBinding `json:"bindings"`
	Controls map[string]codexControlIntent  `json:"controls,omitempty"`
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

const legacyBindingDefaultPlatform = "wechat"

const codexSessionStateVersion = 2

func newCodexSessionStore() *codexSessionStore {
	return &codexSessionStore{
		bindings:   make(map[string]codexSessionBinding),
		controls:   make(map[string]codexControlIntent),
		writeState: writeCodexSessionStateFile,
	}
}

// DefaultCodexSessionFile 返回 Codex workspace/thread 列表的默认持久化路径。
func DefaultCodexSessionFile() string {
	return filepath.Join(defaultDataDir(), "codex-sessions.json")
}

// SetFilePath 设置持久化文件路径并加载已有状态。
func (s *codexSessionStore) SetFilePath(filePath string) {
	s.mu.Lock()
	s.filePath = strings.TrimSpace(filePath)
	s.mu.Unlock()
	s.load()
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

func (s *codexSessionStore) ensureBindingLocked(bindingKey string) codexSessionBinding {
	binding := s.bindings[bindingKey]
	if binding.Workspaces == nil {
		binding.Workspaces = make(map[string]codexWorkspaceSession)
	}
	return binding
}
