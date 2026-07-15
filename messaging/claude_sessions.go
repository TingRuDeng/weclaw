package messaging

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type claudeBindingStatus string

const (
	claudeBindingUnbound       claudeBindingStatus = "unbound"
	claudeBindingPendingResume claudeBindingStatus = "pending_resume"
	claudeBindingReady         claudeBindingStatus = "ready"
	claudeBindingResumeFailed  claudeBindingStatus = "resume_failed"
)

// claudeBindingExecutionKey 统一 Claude 绑定写入、交接和任务登记使用的串行键。
func claudeBindingExecutionKey(bindingKey string) string {
	return "claude-binding:" + bindingKey
}

type claudeSessionBinding struct {
	WorkspaceRoot string              `json:"workspace_root,omitempty"`
	SessionID     string              `json:"session_id,omitempty"`
	Status        claudeBindingStatus `json:"status"`
	UpdatedAt     string              `json:"updated_at"`
}

type claudeSessionState struct {
	Version  int                             `json:"version"`
	Bindings map[string]claudeSessionBinding `json:"bindings"`
	Controls map[string]claudeControlIntent  `json:"controls"`
	Updated  string                          `json:"updated"`
}

type claudeSessionStore struct {
	mu       sync.Mutex
	saveMu   sync.Mutex
	filePath string
	bindings map[string]claudeSessionBinding
	controls map[string]claudeControlIntent
	persist  func(claudeSessionState) error
}

func newClaudeSessionStore() *claudeSessionStore {
	return &claudeSessionStore{
		bindings: make(map[string]claudeSessionBinding),
		controls: make(map[string]claudeControlIntent),
	}
}

// DefaultClaudeSessionFile 返回 Claude route/session 绑定的默认持久化路径。
func DefaultClaudeSessionFile() string {
	return filepath.Join(defaultDataDir(), "claude-sessions.json")
}

// SetFilePath 设置持久化文件并加载状态；损坏文件只记录错误，不覆盖磁盘内容。
func (s *claudeSessionStore) SetFilePath(filePath string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	s.filePath = strings.TrimSpace(filePath)
	path := s.filePath
	s.persist = func(state claudeSessionState) error {
		return persistClaudeSessionState(path, state)
	}
	s.mu.Unlock()
	return s.loadLocked()
}

// binding 返回 route 当前绑定快照。
func (s *claudeSessionStore) binding(bindingKey string) claudeSessionBinding {
	binding, _ := s.bindingSnapshot(bindingKey)
	return binding
}

func (s *claudeSessionStore) bindingSnapshot(bindingKey string) (claudeSessionBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[bindingKey]
	return binding, ok
}

func (s *claudeSessionStore) controlIntent(sessionID string) claudeControlIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controls[strings.TrimSpace(sessionID)]
}

// commitSelection 原子提交已由 ACP 验证成功的 workspace/session 绑定。
func (s *claudeSessionStore) commitSelection(bindingKey string, workspaceRoot string, sessionID string) error {
	workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
	sessionID = strings.TrimSpace(sessionID)
	if workspaceRoot == "" || sessionID == "" {
		return fmt.Errorf("Claude workspace/session 不能为空")
	}
	return s.updateBinding(bindingKey, func(current claudeSessionBinding) claudeSessionBinding {
		return newClaudeBinding(workspaceRoot, sessionID, claudeBindingReady)
	})
}

// markPendingResume 标记重启后需要在首次普通消息前恢复真实 ACP session。
func (s *claudeSessionStore) markPendingResume(bindingKey string) error {
	return s.updateStatus(bindingKey, claudeBindingPendingResume)
}

// markResumeFailed 保留 session ID，阻止普通消息隐式新建或反复恢复。
func (s *claudeSessionStore) markResumeFailed(bindingKey string) error {
	return s.updateStatus(bindingKey, claudeBindingResumeFailed)
}

// markReady 标记 ACP runtime 已恢复当前 session。
func (s *claudeSessionStore) markReady(bindingKey string) error {
	return s.updateStatus(bindingKey, claudeBindingReady)
}

func (s *claudeSessionStore) updateStatus(bindingKey string, status claudeBindingStatus) error {
	return s.updateBinding(bindingKey, func(current claudeSessionBinding) claudeSessionBinding {
		if current.SessionID == "" {
			return current
		}
		current.Status = status
		current.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return current
	})
}

func newClaudeBinding(workspaceRoot string, sessionID string, status claudeBindingStatus) claudeSessionBinding {
	return claudeSessionBinding{
		WorkspaceRoot: workspaceRoot,
		SessionID:     sessionID,
		Status:        status,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
}

// updateBinding 先写入克隆状态，成功后才发布内存，读者不会观察到未落盘状态。
func (s *claudeSessionStore) updateBinding(bindingKey string, mutate func(claudeSessionBinding) claudeSessionBinding) error {
	if strings.TrimSpace(bindingKey) == "" {
		return fmt.Errorf("Claude binding key 不能为空")
	}
	return s.updateBindings(func(bindings map[string]claudeSessionBinding) {
		bindings[bindingKey] = mutate(bindings[bindingKey])
	})
}

func (s *claudeSessionStore) updateBindings(mutate func(map[string]claudeSessionBinding)) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	bindings := cloneClaudeBindings(s.bindings)
	controls := cloneClaudeControls(s.controls)
	mutate(bindings)
	state := newClaudeSessionState(bindings, controls)
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		if err := persist(state); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.bindings = bindings
	s.controls = controls
	s.mu.Unlock()
	return nil
}

func cloneClaudeBindings(input map[string]claudeSessionBinding) map[string]claudeSessionBinding {
	bindings := make(map[string]claudeSessionBinding, len(input))
	for key, binding := range input {
		bindings[key] = binding
	}
	return bindings
}

func newClaudeSessionState(bindings map[string]claudeSessionBinding, controls map[string]claudeControlIntent) claudeSessionState {
	return claudeSessionState{
		Version:  claudeSessionStateVersion,
		Bindings: cloneClaudeBindings(bindings),
		Controls: cloneClaudeControls(controls),
		Updated:  time.Now().UTC().Format(time.RFC3339),
	}
}

func claudeBindingKey(userID string, agentName string) string {
	return normalizeConversationUserKey(userID) + "\x00" + strings.TrimSpace(agentName)
}

func buildClaudeConversationID(userID string, agentName string, workspaceRoot string) string {
	return strings.Join([]string{"claude", normalizeConversationUserKey(userID), strings.TrimSpace(agentName), normalizeClaudeWorkspaceRoot(workspaceRoot)}, "\x00")
}

func normalizeClaudeWorkspaceRoot(workspaceRoot string) string {
	return normalizeCodexWorkspaceRoot(workspaceRoot)
}
