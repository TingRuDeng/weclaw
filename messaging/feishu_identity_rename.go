package messaging

import (
	"fmt"
	"strings"
)

// FeishuIdentityRenameRequest 描述一次本地身份显示名更新。
type FeishuIdentityRenameRequest struct {
	Selector    string
	DisplayName string
	FilePath    string
}

// FeishuIdentityRenameResult 返回被更新的稳定身份和展示名。
type FeishuIdentityRenameResult struct {
	Identity    string
	DisplayName string
}

// RenameFeishuIdentity 只更新本地显示名，不改变任何授权身份。
func RenameFeishuIdentity(req FeishuIdentityRenameRequest) (FeishuIdentityRenameResult, error) {
	selector := strings.TrimSpace(req.Selector)
	displayName := strings.TrimSpace(req.DisplayName)
	if selector == "" || displayName == "" {
		return FeishuIdentityRenameResult{}, fmt.Errorf("用法: weclaw feishu users rename <union_id|user_id|open_id> <显示名>")
	}
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(req.FilePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return FeishuIdentityRenameResult{}, err
	}
	return renameFeishuIdentity(store, selector, displayName)
}

// renameFeishuIdentity 在已加载的身份状态中更新显示名。
func renameFeishuIdentity(store *feishuIdentityStore, selector string, displayName string) (FeishuIdentityRenameResult, error) {
	record, ok := store.Rename(selector, displayName)
	if !ok {
		return FeishuIdentityRenameResult{}, fmt.Errorf("未找到飞书用户身份。")
	}
	return FeishuIdentityRenameResult{
		Identity:    firstNonBlank(preferredFeishuAllowedIdentity(record), record.Key),
		DisplayName: record.DisplayName,
	}, nil
}

// Rename 更新身份记录展示名，保留原始 ID 作为权限判断依据。
func (s *feishuIdentityStore) Rename(selector string, displayName string) (feishuIdentityRecord, bool) {
	selector = strings.TrimSpace(selector)
	displayName = strings.TrimSpace(displayName)
	if selector == "" || displayName == "" {
		return feishuIdentityRecord{}, false
	}
	s.mu.Lock()
	key := s.resolveKeyLocked(selector)
	if key == "" {
		s.mu.Unlock()
		return feishuIdentityRecord{}, false
	}
	record := s.records[key]
	record.DisplayName = displayName
	s.records[key] = record
	s.mu.Unlock()
	s.save()
	return copyFeishuIdentityRecord(record), true
}
