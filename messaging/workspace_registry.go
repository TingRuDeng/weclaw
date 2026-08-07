package messaging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const workspaceRegistryVersion = 2

type workspaceRegistryEntry struct {
	Root     string `json:"root"`
	AddedAt  string `json:"added_at,omitempty"`
	HiddenAt string `json:"hidden_at,omitempty"`
}

type workspaceRegistryAgentState struct {
	Registered     []workspaceRegistryEntry        `json:"registered,omitempty"`
	Hidden         []workspaceRegistryEntry        `json:"hidden,omitempty"`
	HiddenSessions []workspaceRegistrySessionEntry `json:"hidden_sessions,omitempty"`
}

type workspaceRegistrySessionEntry struct {
	ID       string `json:"id"`
	HiddenAt string `json:"hidden_at,omitempty"`
}

type workspaceRegistryState struct {
	Version  int                                    `json:"version"`
	Revision uint64                                 `json:"revision"`
	Agents   map[string]workspaceRegistryAgentState `json:"agents"`
	Updated  string                                 `json:"updated,omitempty"`
}

type workspaceRegistrySnapshot struct {
	Revision       uint64
	Registered     []workspaceRegistryEntry
	Hidden         map[string]struct{}
	HiddenSessions map[string]struct{}
}

func (s workspaceRegistrySnapshot) IsSessionHidden(sessionID string) bool {
	_, hidden := s.HiddenSessions[strings.TrimSpace(sessionID)]
	return hidden
}

func (s workspaceRegistrySnapshot) IsHidden(root string) bool {
	canonical, err := canonicalWorkspaceRegistryPath(root, false)
	if err != nil {
		return false
	}
	_, hidden := s.Hidden[canonical]
	return hidden
}

type workspaceRegistryMutation struct {
	Root     string
	Changed  bool
	Revision uint64
}

type workspaceRegistrySessionMutation struct {
	SessionID string
	Changed   bool
	Revision  uint64
}

type workspaceRegistry struct {
	control  sync.Mutex
	mu       sync.RWMutex
	state    workspaceRegistryState
	filePath string
	loadErr  error
	now      func() time.Time
	persist  func(string, workspaceRegistryState) error
}

func newWorkspaceRegistry() *workspaceRegistry {
	return &workspaceRegistry{
		state:   newWorkspaceRegistryState(),
		now:     time.Now,
		persist: persistWorkspaceRegistryState,
	}
}

func newWorkspaceRegistryState() workspaceRegistryState {
	return workspaceRegistryState{
		Version: workspaceRegistryVersion,
		Agents:  make(map[string]workspaceRegistryAgentState),
	}
}

func (r *workspaceRegistry) SetFilePath(filePath string) error {
	filePath = strings.TrimSpace(filePath)
	state, err := loadWorkspaceRegistryState(filePath)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.filePath = filePath
	if err != nil {
		r.loadErr = err
		return err
	}
	r.state = state
	r.loadErr = nil
	return nil
}

func (r *workspaceRegistry) Snapshot(agentName string) (workspaceRegistrySnapshot, error) {
	agentName = strings.TrimSpace(agentName)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.loadErr != nil {
		return workspaceRegistrySnapshot{}, fmt.Errorf("工作空间登记状态不可用: %w", r.loadErr)
	}
	agentState := r.state.Agents[agentName]
	snapshot := workspaceRegistrySnapshot{
		Revision:       r.state.Revision,
		Registered:     append([]workspaceRegistryEntry(nil), agentState.Registered...),
		Hidden:         make(map[string]struct{}, len(agentState.Hidden)),
		HiddenSessions: make(map[string]struct{}, len(agentState.HiddenSessions)),
	}
	for _, entry := range agentState.Hidden {
		snapshot.Hidden[entry.Root] = struct{}{}
	}
	for _, entry := range agentState.HiddenSessions {
		snapshot.HiddenSessions[entry.ID] = struct{}{}
	}
	return snapshot, nil
}

func (r *workspaceRegistry) HideSession(agentName string, sessionID string) (workspaceRegistrySessionMutation, error) {
	return r.mutateSession(agentName, sessionID, true)
}

func (r *workspaceRegistry) RestoreSession(agentName string, sessionID string) (workspaceRegistrySessionMutation, error) {
	return r.mutateSession(agentName, sessionID, false)
}

func (r *workspaceRegistry) mutateSession(agentName string, sessionID string, hide bool) (workspaceRegistrySessionMutation, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return workspaceRegistrySessionMutation{}, fmt.Errorf("Agent 名称不能为空")
	}
	sessionID, err := normalizeWorkspaceRegistrySessionID(sessionID)
	if err != nil {
		return workspaceRegistrySessionMutation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return workspaceRegistrySessionMutation{}, fmt.Errorf("工作空间登记状态不可用: %w", r.loadErr)
	}
	if r.filePath == "" {
		return workspaceRegistrySessionMutation{}, fmt.Errorf("工作空间登记状态文件未配置")
	}
	next := cloneWorkspaceRegistryState(r.state)
	agentState := next.Agents[agentName]
	changed := false
	if hide {
		if !containsWorkspaceRegistrySessionEntry(agentState.HiddenSessions, sessionID) {
			agentState.HiddenSessions = append(agentState.HiddenSessions, workspaceRegistrySessionEntry{
				ID: sessionID, HiddenAt: r.now().UTC().Format(time.RFC3339),
			})
			changed = true
		}
	} else {
		agentState.HiddenSessions, changed = removeWorkspaceRegistrySessionEntry(agentState.HiddenSessions, sessionID)
	}
	if !changed {
		return workspaceRegistrySessionMutation{SessionID: sessionID, Revision: r.state.Revision}, nil
	}
	sortWorkspaceRegistrySessionEntries(agentState.HiddenSessions)
	next.Agents[agentName] = agentState
	next.Version = workspaceRegistryVersion
	next.Revision++
	next.Updated = r.now().UTC().Format(time.RFC3339)
	if err := r.persist(r.filePath, next); err != nil {
		return workspaceRegistrySessionMutation{}, fmt.Errorf("保存工作空间登记状态失败: %w", err)
	}
	r.state = next
	return workspaceRegistrySessionMutation{SessionID: sessionID, Changed: true, Revision: next.Revision}, nil
}

func (r *workspaceRegistry) Add(agentName string, root string) (workspaceRegistryMutation, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return workspaceRegistryMutation{}, fmt.Errorf("Agent 名称不能为空")
	}
	canonical, err := canonicalWorkspaceRegistryPath(root, true)
	if err != nil {
		return workspaceRegistryMutation{}, err
	}
	return r.mutate(agentName, canonical, true)
}

func (r *workspaceRegistry) Remove(agentName string, root string) (workspaceRegistryMutation, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return workspaceRegistryMutation{}, fmt.Errorf("Agent 名称不能为空")
	}
	canonical, err := canonicalWorkspaceRegistryPath(root, false)
	if err != nil {
		return workspaceRegistryMutation{}, err
	}
	return r.mutate(agentName, canonical, false)
}

func (r *workspaceRegistry) mutate(agentName string, root string, add bool) (workspaceRegistryMutation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return workspaceRegistryMutation{}, fmt.Errorf("工作空间登记状态不可用: %w", r.loadErr)
	}
	if r.filePath == "" {
		return workspaceRegistryMutation{}, fmt.Errorf("工作空间登记状态文件未配置")
	}
	next := cloneWorkspaceRegistryState(r.state)
	agentState := next.Agents[agentName]
	changed := false
	if add {
		agentState.Hidden, changed = removeWorkspaceRegistryEntry(agentState.Hidden, root)
		if !containsWorkspaceRegistryEntry(agentState.Registered, root) {
			agentState.Registered = append(agentState.Registered, workspaceRegistryEntry{
				Root: root, AddedAt: r.now().UTC().Format(time.RFC3339),
			})
			changed = true
		}
	} else {
		var removed bool
		agentState.Registered, removed = removeWorkspaceRegistryEntry(agentState.Registered, root)
		changed = changed || removed
		if !containsWorkspaceRegistryEntry(agentState.Hidden, root) {
			agentState.Hidden = append(agentState.Hidden, workspaceRegistryEntry{
				Root: root, HiddenAt: r.now().UTC().Format(time.RFC3339),
			})
			changed = true
		}
	}
	if !changed {
		return workspaceRegistryMutation{Root: root, Revision: r.state.Revision}, nil
	}
	sortWorkspaceRegistryEntries(agentState.Registered, true)
	sortWorkspaceRegistryEntries(agentState.Hidden, false)
	next.Agents[agentName] = agentState
	next.Revision++
	next.Updated = r.now().UTC().Format(time.RFC3339)
	if err := r.persist(r.filePath, next); err != nil {
		return workspaceRegistryMutation{}, fmt.Errorf("保存工作空间登记状态失败: %w", err)
	}
	r.state = next
	return workspaceRegistryMutation{Root: root, Changed: true, Revision: next.Revision}, nil
}

func canonicalWorkspaceRegistryPath(path string, mustExist bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("工作空间路径不能为空")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		}
	}
	canonical, err := canonicalizePath(path, mustExist)
	if err != nil {
		return "", fmt.Errorf("工作空间路径不存在: %s", path)
	}
	if mustExist {
		info, statErr := os.Stat(canonical)
		if statErr != nil {
			return "", fmt.Errorf("工作空间路径不存在: %s", canonical)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("工作空间路径不是目录: %s", canonical)
		}
	}
	return canonical, nil
}

func cloneWorkspaceRegistryState(state workspaceRegistryState) workspaceRegistryState {
	clone := workspaceRegistryState{
		Version: state.Version, Revision: state.Revision, Updated: state.Updated,
		Agents: make(map[string]workspaceRegistryAgentState, len(state.Agents)),
	}
	for name, agentState := range state.Agents {
		clone.Agents[name] = workspaceRegistryAgentState{
			Registered:     append([]workspaceRegistryEntry(nil), agentState.Registered...),
			Hidden:         append([]workspaceRegistryEntry(nil), agentState.Hidden...),
			HiddenSessions: append([]workspaceRegistrySessionEntry(nil), agentState.HiddenSessions...),
		}
	}
	return clone
}

func normalizeWorkspaceRegistrySessionID(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("会话 ID 不能为空")
	}
	if len(sessionID) > 1024 {
		return "", fmt.Errorf("会话 ID 过长")
	}
	for _, char := range sessionID {
		if unicode.IsControl(char) {
			return "", fmt.Errorf("会话 ID 包含控制字符")
		}
	}
	return sessionID, nil
}

func containsWorkspaceRegistrySessionEntry(entries []workspaceRegistrySessionEntry, sessionID string) bool {
	for _, entry := range entries {
		if entry.ID == sessionID {
			return true
		}
	}
	return false
}

func removeWorkspaceRegistrySessionEntry(entries []workspaceRegistrySessionEntry, sessionID string) ([]workspaceRegistrySessionEntry, bool) {
	result := make([]workspaceRegistrySessionEntry, 0, len(entries))
	removed := false
	for _, entry := range entries {
		if entry.ID == sessionID {
			removed = true
			continue
		}
		result = append(result, entry)
	}
	return result, removed
}

func sortWorkspaceRegistrySessionEntries(entries []workspaceRegistrySessionEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].HiddenAt != entries[j].HiddenAt {
			return entries[i].HiddenAt < entries[j].HiddenAt
		}
		return entries[i].ID < entries[j].ID
	})
}

func containsWorkspaceRegistryEntry(entries []workspaceRegistryEntry, root string) bool {
	for _, entry := range entries {
		if entry.Root == root {
			return true
		}
	}
	return false
}

func removeWorkspaceRegistryEntry(entries []workspaceRegistryEntry, root string) ([]workspaceRegistryEntry, bool) {
	result := make([]workspaceRegistryEntry, 0, len(entries))
	removed := false
	for _, entry := range entries {
		if entry.Root == root {
			removed = true
			continue
		}
		result = append(result, entry)
	}
	return result, removed
}

func sortWorkspaceRegistryEntries(entries []workspaceRegistryEntry, registered bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i].HiddenAt, entries[j].HiddenAt
		if registered {
			left, right = entries[i].AddedAt, entries[j].AddedAt
		}
		if left != right {
			return left < right
		}
		return entries[i].Root < entries[j].Root
	})
}

func mergeWorkspaceRegistryGroups(native []codexWorkspaceGroup, snapshot workspaceRegistrySnapshot) []codexWorkspaceGroup {
	result := make([]codexWorkspaceGroup, 0, len(native)+len(snapshot.Registered))
	seen := make(map[string]struct{}, len(native)+len(snapshot.Registered))
	hiddenRoots := make(map[string]struct{}, len(snapshot.Hidden))
	for root := range snapshot.Hidden {
		canonical, err := canonicalWorkspaceRegistryPath(root, false)
		if err == nil {
			hiddenRoots[canonical] = struct{}{}
		}
	}
	for _, group := range native {
		root, err := canonicalWorkspaceRegistryPath(group.Root, false)
		if err != nil {
			continue
		}
		if _, hidden := hiddenRoots[root]; hidden {
			continue
		}
		if _, duplicate := seen[root]; duplicate {
			continue
		}
		result = append(result, group)
		seen[root] = struct{}{}
	}
	registered := append([]workspaceRegistryEntry(nil), snapshot.Registered...)
	sortWorkspaceRegistryEntries(registered, true)
	for _, entry := range registered {
		root, err := canonicalWorkspaceRegistryPath(entry.Root, true)
		if err != nil {
			continue
		}
		if _, hidden := hiddenRoots[root]; hidden {
			continue
		}
		if _, duplicate := seen[root]; duplicate {
			continue
		}
		result = append(result, codexWorkspaceGroup{Name: shortCodexWorkspaceName(root), Root: root})
		seen[root] = struct{}{}
	}
	return result
}
