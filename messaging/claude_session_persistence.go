package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
	bindings, migrated, err := decodeClaudeSessionState(data)
	if err != nil {
		return fmt.Errorf("解析 Claude 状态失败: %w", err)
	}
	s.mu.Lock()
	persist := s.persist
	s.mu.Unlock()
	if migrated && persist != nil {
		if err := persist(newClaudeSessionState(bindings)); err != nil {
			return fmt.Errorf("保存 Claude 迁移状态失败: %w", err)
		}
	}
	s.mu.Lock()
	s.bindings = bindings
	s.mu.Unlock()
	return nil
}

func decodeClaudeSessionState(data []byte) (map[string]claudeSessionBinding, bool, error) {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, false, err
	}
	if header.Version == 1 {
		bindings, err := migrateClaudeSessionV1(data)
		return bindings, true, err
	}
	if header.Version != 2 && header.Version != 3 && header.Version != claudeSessionStateVersion {
		return nil, false, fmt.Errorf("不支持的 Claude 状态版本: %d", header.Version)
	}
	var state claudeSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, false, err
	}
	bindings := normalizeClaudeBindings(state.Bindings)
	return bindings, header.Version != claudeSessionStateVersion || !sameClaudeBindingMap(bindings, state.Bindings), nil
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
		previous := binding
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
		if binding.Revision == 0 {
			binding.Revision = 1
		} else if binding.Status != previous.Status || binding.WorkspaceRoot != previous.WorkspaceRoot || binding.SessionID != previous.SessionID {
			binding.Revision++
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
