package messaging

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type codexSessionStateWriter func(filePath string, data []byte) error

type codexSessionStore struct {
	mu         sync.Mutex
	saveMu     sync.Mutex
	filePath   string
	bindings   map[string]codexSessionBinding
	archived   map[string]struct{}
	writeState codexSessionStateWriter
}

type codexSessionState struct {
	Version  int                            `json:"version"`
	Bindings map[string]codexSessionBinding `json:"bindings"`
	Archived []string                       `json:"archived,omitempty"`
	// Controls is read only to migrate v1-v3 state. v4+ never writes it.
	Controls map[string]legacyCodexControlIntent `json:"controls,omitempty"`
	Updated  string                              `json:"updated"`
}

type legacyCodexControlIntent struct {
	Owner           string `json:"owner"`
	RouteBindingKey string `json:"routeBindingKey,omitempty"`
	ConversationID  string `json:"conversationId,omitempty"`
	Revision        uint64 `json:"revision"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

type codexSessionBinding struct {
	ActiveWorkspace string
	Workspaces      map[string]codexWorkspaceSession
	FollowRevision  uint64
	Follower        *codexFrontendFollower
}

type codexFrontendFollower struct {
	WorkspaceRoot string
	ThreadID      string
	ActorUserID   string
	DeliveryRoute platform.DeliveryRoute
	UpdatedAt     string
}

type codexFollowerSnapshot struct {
	BindingKey     string
	RouteUserID    string
	AgentName      string
	ConversationID string
	Revision       uint64
	// RecoveryThreadID 精确指向首次补建前的 placeholder thread，供跨进程修复 outbox trace。
	RecoveryThreadID      string
	RecoveryReservationID string
	Target                codexFrontendFollower
}

type codexWorkspaceSession struct {
	ThreadID                       string
	PendingNewThread               bool
	PendingFirstTurn               bool
	FirstTurnRecoveryThreadID      string
	FirstTurnRecoveryReservationID string
	ReleasePending                 bool
	Released                       bool
	ReleasedThreadID               string
	ReleasedRecoveryThreadID       string
	ReleasedRecoveryReservationID  string
	UpdatedAt                      string
}

const legacyBindingDefaultPlatform = "wechat"

// v9 persists two-phase frontend release intent and its exact active-card recovery reservation.
// v8 added long-lived Feishu follower endpoints, release/archive tombstones, and the predecessor
// thread needed to repair first-turn outbox metadata after a crash. Codex writer authority belongs to
// the single app-server and is never assigned to a message route.
const codexSessionStateVersion = 9

func codexWorkspaceReleaseIntent(session codexWorkspaceSession) bool {
	return session.ReleasePending || session.Released
}

func clearCodexWorkspaceReleaseState(session *codexWorkspaceSession) {
	if session == nil {
		return
	}
	session.ReleasePending = false
	session.Released = false
	session.ReleasedThreadID = ""
	session.ReleasedRecoveryThreadID = ""
	session.ReleasedRecoveryReservationID = ""
}

func newCodexSessionStore() *codexSessionStore {
	return &codexSessionStore{
		bindings:   make(map[string]codexSessionBinding),
		archived:   make(map[string]struct{}),
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
	if codexWorkspaceReleaseIntent(session) {
		return "", false
	}
	if _, archived := s.archived[strings.TrimSpace(session.ThreadID)]; archived {
		return "", false
	}
	return session.ThreadID, session.PendingNewThread
}

func (s *codexSessionStore) workspaceReleased(bindingKey string, workspaceRoot string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	return codexWorkspaceReleaseIntent(s.bindings[bindingKey].Workspaces[workspaceRoot])
}

func (s *codexSessionStore) getActiveWorkspace(bindingKey string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot := normalizeCodexWorkspaceRoot(s.bindings[bindingKey].ActiveWorkspace)
	return workspaceRoot, workspaceRoot != ""
}

func (s *codexSessionStore) workspaceInUse(workspaceRoot string) bool {
	workspaceRoot, _ = canonicalWorkspaceRegistryPath(workspaceRoot, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, binding := range s.bindings {
		candidate, _ := canonicalWorkspaceRegistryPath(binding.ActiveWorkspace, false)
		if candidate != "" && candidate == workspaceRoot {
			return true
		}
	}
	return false
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
		PendingFirstTurn: false,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *codexSessionStore) setPendingNew(bindingKey string, workspaceRoot string) {
	s.updateWorkspace(bindingKey, workspaceRoot, codexWorkspaceSession{
		ThreadID:         "",
		PendingNewThread: true,
		PendingFirstTurn: false,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *codexSessionStore) isPendingFirstTurn(bindingKey string, workspaceRoot string, threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	session := s.bindings[bindingKey].Workspaces[workspaceRoot]
	return session.PendingFirstTurn && strings.TrimSpace(session.ThreadID) == strings.TrimSpace(threadID)
}

// clearPendingFirstTurn 在首个 turn 已被 Codex 接受后清除空会话恢复标记。
func (s *codexSessionStore) clearPendingFirstTurn(bindingKey string, workspaceRoot string, threadID string) bool {
	s.mu.Lock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.bindings[bindingKey]
	session, ok := binding.Workspaces[workspaceRoot]
	if !ok || !session.PendingFirstTurn || strings.TrimSpace(session.ThreadID) != strings.TrimSpace(threadID) {
		s.mu.Unlock()
		return false
	}
	session.PendingFirstTurn = false
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	binding.Workspaces[workspaceRoot] = session
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
	return true
}

func (s *codexSessionStore) clearFirstTurnRecoveryJournal(
	bindingKey string,
	workspaceRoot string,
	threadID string,
	recoveryThreadID string,
	recoveryReservationID string,
) bool {
	s.mu.Lock()
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	binding := s.bindings[bindingKey]
	session, ok := binding.Workspaces[workspaceRoot]
	if !ok || strings.TrimSpace(session.ThreadID) != strings.TrimSpace(threadID) ||
		strings.TrimSpace(session.FirstTurnRecoveryThreadID) != strings.TrimSpace(recoveryThreadID) ||
		strings.TrimSpace(session.FirstTurnRecoveryReservationID) != strings.TrimSpace(recoveryReservationID) {
		s.mu.Unlock()
		return false
	}
	session.FirstTurnRecoveryThreadID = ""
	session.FirstTurnRecoveryReservationID = ""
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	binding.Workspaces[workspaceRoot] = session
	s.bindings[bindingKey] = binding
	s.mu.Unlock()
	s.save()
	return true
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
