package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *claudeSessionStore) load() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.loadLocked()
}

func (s *claudeSessionStore) loadLocked() error {
	s.mu.Lock()
	path := s.filePath
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("读取 Claude 状态失败: %w", err)
		}
		return nil
	}
	bindings, controls, migrated, err := decodeClaudeSessionState(data)
	if err != nil {
		return fmt.Errorf("解析 Claude 状态失败: %w", err)
	}
	s.mu.Lock()
	persist := s.persist
	s.mu.Unlock()
	if migrated && persist != nil {
		if err := persist(newClaudeSessionState(bindings, controls)); err != nil {
			return fmt.Errorf("保存 Claude 迁移状态失败: %w", err)
		}
	}
	s.mu.Lock()
	s.bindings = bindings
	s.controls = controls
	s.mu.Unlock()
	return nil
}

func decodeClaudeSessionState(data []byte) (map[string]claudeSessionBinding, map[string]claudeControlIntent, bool, error) {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, nil, false, err
	}
	if header.Version == 1 {
		bindings, err := migrateClaudeSessionV1(data)
		return bindings, map[string]claudeControlIntent{}, true, err
	}
	if header.Version != 2 && header.Version != claudeSessionStateVersion {
		return nil, nil, false, fmt.Errorf("不支持的 Claude 状态版本: %d", header.Version)
	}
	var state claudeSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, nil, false, err
	}
	bindings := normalizeClaudeBindings(state.Bindings)
	if header.Version == 2 {
		return bindings, migrateClaudeControlsV2(bindings), true, nil
	}
	return bindings, normalizeClaudeControlsForBindings(bindings, state.Controls), false, nil
}

func migrateClaudeControlsV2(bindings map[string]claudeSessionBinding) map[string]claudeControlIntent {
	references := make(map[string][]claudeBindingReference)
	for key, binding := range bindings {
		if binding.SessionID == "" || binding.WorkspaceRoot == "" {
			continue
		}
		references[binding.SessionID] = append(references[binding.SessionID], claudeBindingReference{bindingKey: key, binding: binding})
	}
	controls := make(map[string]claudeControlIntent, len(references))
	for sessionID, refs := range references {
		if len(refs) != 1 {
			controls[sessionID] = newMigratedClaudeControl(claudeOwnerUnclaimed, "", "", latestClaudeBindingUpdate(refs))
			continue
		}
		ref := refs[0]
		conversationID := claudeConversationIDForBinding(ref.bindingKey, ref.binding.WorkspaceRoot)
		controls[sessionID] = newMigratedClaudeControl(
			claudeOwnerRemote, ref.bindingKey, conversationID, ref.binding.UpdatedAt,
		)
	}
	return controls
}

type claudeBindingReference struct {
	bindingKey string
	binding    claudeSessionBinding
}

func claudeConversationIDForBinding(bindingKey string, workspaceRoot string) string {
	parts := strings.SplitN(bindingKey, "\x00", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return ""
	}
	return buildClaudeConversationID(parts[0], parts[1], workspaceRoot)
}

func latestClaudeBindingUpdate(refs []claudeBindingReference) string {
	latest := ""
	for _, ref := range refs {
		if ref.binding.UpdatedAt > latest {
			latest = ref.binding.UpdatedAt
		}
	}
	return latest
}

func normalizeClaudeControls(input map[string]claudeControlIntent) map[string]claudeControlIntent {
	controls := make(map[string]claudeControlIntent, len(input))
	for sessionID, intent := range input {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		controls[sessionID] = normalizeClaudeControlIntent(intent)
	}
	return controls
}

func normalizeClaudeControlsForBindings(
	bindings map[string]claudeSessionBinding,
	input map[string]claudeControlIntent,
) map[string]claudeControlIntent {
	controls := normalizeClaudeControls(input)
	remoteSessionsByBinding := make(map[string][]string)
	for sessionID, intent := range controls {
		if intent.Owner == claudeOwnerRemote {
			remoteSessionsByBinding[intent.BindingKey] = append(remoteSessionsByBinding[intent.BindingKey], sessionID)
		}
	}
	for sessionID, intent := range controls {
		if intent.Owner != claudeOwnerRemote {
			continue
		}
		binding, ok := bindings[intent.BindingKey]
		conflictingBinding := len(remoteSessionsByBinding[intent.BindingKey]) != 1
		expectedConversationID := ""
		if ok {
			expectedConversationID = claudeConversationIDForBinding(intent.BindingKey, binding.WorkspaceRoot)
		}
		if conflictingBinding || !ok || binding.SessionID != sessionID || expectedConversationID == "" ||
			intent.ConversationID != expectedConversationID {
			controls[sessionID] = normalizeClaudeControlIntent(claudeControlIntent{
				Owner: claudeOwnerUnclaimed, Revision: intent.Revision, UpdatedAt: intent.UpdatedAt,
			})
		}
	}
	return controls
}

// migrateClaudeSessionV1 只迁移浏览工作空间，丢弃旧 CLI session ID。
func migrateClaudeSessionV1(data []byte) (map[string]claudeSessionBinding, error) {
	var legacy codexSessionState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}
	bindings := make(map[string]claudeSessionBinding, len(legacy.Bindings))
	keys := make([]string, 0, len(legacy.Bindings))
	for key := range legacy.Bindings {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		_, leftMigrated := migrateLegacyBindingKey(keys[i])
		_, rightMigrated := migrateLegacyBindingKey(keys[j])
		if leftMigrated != rightMigrated {
			return leftMigrated
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		old := legacy.Bindings[key]
		workspaceRoot := normalizeClaudeWorkspaceRoot(old.ActiveWorkspace)
		if strings.TrimSpace(key) == "" || workspaceRoot == "" {
			continue
		}
		migratedKey, _ := migrateLegacyBindingKey(key)
		bindings[migratedKey] = newClaudeBinding(workspaceRoot, "", claudeBindingUnbound)
	}
	return bindings, nil
}

// normalizeClaudeBindings 将进程重启后的有效 session 统一置为 pending_resume。
func normalizeClaudeBindings(input map[string]claudeSessionBinding) map[string]claudeSessionBinding {
	bindings := make(map[string]claudeSessionBinding, len(input))
	for key, binding := range input {
		binding.WorkspaceRoot = normalizeClaudeWorkspaceRoot(binding.WorkspaceRoot)
		binding.SessionID = strings.TrimSpace(binding.SessionID)
		if strings.TrimSpace(key) == "" || binding.WorkspaceRoot == "" {
			continue
		}
		if binding.SessionID == "" {
			binding.Status = claudeBindingUnbound
		} else {
			binding.Status = claudeBindingPendingResume
		}
		bindings[key] = binding
	}
	return bindings
}

func persistClaudeSessionState(path string, state claudeSessionState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建 Claude 状态目录失败: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 Claude 状态失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("创建 Claude 状态临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeClaudeSessionTemp(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 Claude 状态文件失败: %w", err)
	}
	return nil
}

func writeClaudeSessionTemp(tmp *os.File, data []byte) error {
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}
