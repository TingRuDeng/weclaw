package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func DefaultWorkspaceRegistryFile() string {
	return filepath.Join(defaultDataDir(), "workspace-registry.json")
}

func loadWorkspaceRegistryState(filePath string) (workspaceRegistryState, error) {
	state := newWorkspaceRegistryState()
	if strings.TrimSpace(filePath) == "" {
		return state, nil
	}
	data, err := os.ReadFile(filePath)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return workspaceRegistryState{}, fmt.Errorf("读取工作空间登记状态失败: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return workspaceRegistryState{}, fmt.Errorf("解析工作空间登记状态失败: %w", err)
	}
	if state.Version != 1 && state.Version != workspaceRegistryVersion {
		return workspaceRegistryState{}, fmt.Errorf("不支持的工作空间登记状态版本: %d", state.Version)
	}
	normalized, err := normalizeWorkspaceRegistryState(state)
	if err != nil {
		return workspaceRegistryState{}, err
	}
	return normalized, nil
}

func normalizeWorkspaceRegistryState(state workspaceRegistryState) (workspaceRegistryState, error) {
	normalized := workspaceRegistryState{
		Version: workspaceRegistryVersion, Revision: state.Revision, Updated: strings.TrimSpace(state.Updated),
		Agents: make(map[string]workspaceRegistryAgentState, len(state.Agents)),
	}
	names := make([]string, 0, len(state.Agents))
	for name := range state.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return workspaceRegistryState{}, fmt.Errorf("工作空间登记状态包含空 Agent 名称")
		}
		entryState := state.Agents[rawName]
		hidden, err := normalizeWorkspaceRegistryEntries(entryState.Hidden, false)
		if err != nil {
			return workspaceRegistryState{}, fmt.Errorf("Agent %q hidden 状态无效: %w", name, err)
		}
		hiddenRoots := make(map[string]struct{}, len(hidden))
		for _, entry := range hidden {
			hiddenRoots[entry.Root] = struct{}{}
		}
		registered, err := normalizeWorkspaceRegistryEntries(entryState.Registered, true)
		if err != nil {
			return workspaceRegistryState{}, fmt.Errorf("Agent %q registered 状态无效: %w", name, err)
		}
		visible := registered[:0]
		for _, entry := range registered {
			if _, isHidden := hiddenRoots[entry.Root]; !isHidden {
				visible = append(visible, entry)
			}
		}
		hiddenSessions, err := normalizeWorkspaceRegistrySessionEntries(entryState.HiddenSessions)
		if err != nil {
			return workspaceRegistryState{}, fmt.Errorf("Agent %q hidden_sessions 状态无效: %w", name, err)
		}
		normalized.Agents[name] = workspaceRegistryAgentState{
			Registered: visible, Hidden: hidden, HiddenSessions: hiddenSessions,
		}
	}
	return normalized, nil
}

func normalizeWorkspaceRegistrySessionEntries(entries []workspaceRegistrySessionEntry) ([]workspaceRegistrySessionEntry, error) {
	normalized := make([]workspaceRegistrySessionEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		id, err := normalizeWorkspaceRegistrySessionID(entry.ID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, workspaceRegistrySessionEntry{ID: id, HiddenAt: strings.TrimSpace(entry.HiddenAt)})
	}
	sortWorkspaceRegistrySessionEntries(normalized)
	return normalized, nil
}

func normalizeWorkspaceRegistryEntries(entries []workspaceRegistryEntry, registered bool) ([]workspaceRegistryEntry, error) {
	normalized := make([]workspaceRegistryEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !filepath.IsAbs(strings.TrimSpace(entry.Root)) {
			return nil, fmt.Errorf("工作空间路径必须是绝对路径: %q", entry.Root)
		}
		root, err := canonicalWorkspaceRegistryPath(entry.Root, false)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[root]; duplicate {
			continue
		}
		seen[root] = struct{}{}
		entry.Root = root
		entry.AddedAt = strings.TrimSpace(entry.AddedAt)
		entry.HiddenAt = strings.TrimSpace(entry.HiddenAt)
		if registered {
			entry.HiddenAt = ""
		} else {
			entry.AddedAt = ""
		}
		normalized = append(normalized, entry)
	}
	sortWorkspaceRegistryEntries(normalized, registered)
	return normalized, nil
}

func persistWorkspaceRegistryState(filePath string, state workspaceRegistryState) error {
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("工作空间登记状态文件未配置")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("创建工作空间登记状态目录失败: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码工作空间登记状态失败: %w", err)
	}
	if err := writeAtomic0600(filePath, data); err != nil {
		return err
	}
	return nil
}
