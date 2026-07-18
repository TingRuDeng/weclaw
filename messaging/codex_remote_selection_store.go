package messaging

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var errCodexRemoteSelectionChanged = errors.New("Codex 会话绑定状态已变化")

// codexRemoteSelectionSnapshot is the compare-and-swap image for one
// frontend. Other frontends are deliberately absent: they may bind the same
// app-server thread concurrently.
type codexRemoteSelectionSnapshot struct {
	TargetThreadID string
	Binding        codexSessionBinding
}

type codexRemoteSelectionState struct {
	bindings map[string]codexSessionBinding
}

type codexRemoteSelectionUpdate struct {
	BindingKey       string
	WorkspaceRoot    string
	TargetThreadID   string
	ConversationID   string
	PendingFirstTurn bool
	Expected         codexRemoteSelectionSnapshot
}

type codexRemoteSelectionResult struct {
	before codexRemoteSelectionState
	after  codexRemoteSelectionState
}

// remoteSelectionSnapshot returns only the caller frontend's binding.
func (s *codexSessionStore) remoteSelectionSnapshot(bindingKey string, targetThreadID string) codexRemoteSelectionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return remoteSelectionSnapshotLocked(s.bindings, bindingKey, targetThreadID)
}

// codexRemoteSelectionThreadIDs returns the sole runtime thread touched by a
// binding operation. It remains a slice so the shared lock helper can be used.
func codexRemoteSelectionThreadIDs(snapshot codexRemoteSelectionSnapshot) []string {
	return sortedUniqueCodexThreadIDs([]string{snapshot.TargetThreadID})
}

// commitRemoteSelection persists a candidate before replacing the live map.
// It never reads or mutates a global owner record.
func (s *codexSessionStore) commitRemoteSelection(update codexRemoteSelectionUpdate) (codexRemoteSelectionResult, error) {
	update = normalizeCodexRemoteSelectionUpdate(update)
	if err := validateCodexRemoteSelectionUpdate(update); err != nil {
		return codexRemoteSelectionResult{}, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateCodexRemoteSelectionSnapshotLocked(s.bindings, update); err != nil {
		return codexRemoteSelectionResult{}, err
	}
	before := codexRemoteSelectionState{bindings: cloneCodexSessionBindings(s.bindings)}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	now := time.Now().UTC()
	changed := selectCodexRemoteWorkspace(nextBindings, update, now)
	result := codexRemoteSelectionResult{
		before: before,
		after:  codexRemoteSelectionState{bindings: cloneCodexSessionBindings(nextBindings)},
	}
	if !changed {
		return result, nil
	}
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings,
		Updated: now.Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return codexRemoteSelectionResult{}, fmt.Errorf("保存 Codex 会话绑定: %w", err)
	}
	s.bindings = nextBindings
	return result, nil
}

// rollbackRemoteSelection restores the prior frontend binding only when no
// newer operation has replaced the just-committed state.
func (s *codexSessionStore) rollbackRemoteSelection(result codexRemoteSelectionResult) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameCodexSessionBindings(s.bindings, result.after.bindings) {
		return errCodexRemoteSelectionChanged
	}
	bindings := cloneCodexSessionBindings(result.before.bindings)
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: bindings,
		Updated: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return fmt.Errorf("回滚 Codex 会话绑定: %w", err)
	}
	s.bindings = bindings
	return nil
}

func normalizeCodexRemoteSelectionUpdate(update codexRemoteSelectionUpdate) codexRemoteSelectionUpdate {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeCodexWorkspaceRoot(update.WorkspaceRoot)
	update.TargetThreadID = strings.TrimSpace(update.TargetThreadID)
	update.ConversationID = strings.TrimSpace(update.ConversationID)
	return update
}

func validateCodexRemoteSelectionUpdate(update codexRemoteSelectionUpdate) error {
	if update.BindingKey == "" || update.WorkspaceRoot == "" ||
		update.TargetThreadID == "" || update.ConversationID == "" {
		return fmt.Errorf("Codex 会话绑定缺少必要路由字段")
	}
	if update.Expected.TargetThreadID != update.TargetThreadID {
		return errCodexRemoteSelectionChanged
	}
	return nil
}

func validateCodexRemoteSelectionSnapshotLocked(bindings map[string]codexSessionBinding, update codexRemoteSelectionUpdate) error {
	current := remoteSelectionSnapshotLocked(bindings, update.BindingKey, update.TargetThreadID)
	if !sameCodexRemoteSelectionSnapshot(current, update.Expected) {
		return errCodexRemoteSelectionChanged
	}
	return nil
}

func sameCodexRemoteSelectionSnapshot(left codexRemoteSelectionSnapshot, right codexRemoteSelectionSnapshot) bool {
	return left.TargetThreadID == right.TargetThreadID && sameCodexSessionBinding(left.Binding, right.Binding)
}

func cloneCodexSessionBindings(source map[string]codexSessionBinding) map[string]codexSessionBinding {
	cloned := make(map[string]codexSessionBinding, len(source))
	for key, binding := range source {
		workspaces := make(map[string]codexWorkspaceSession, len(binding.Workspaces))
		for workspaceRoot, session := range binding.Workspaces {
			workspaces[workspaceRoot] = session
		}
		cloned[key] = codexSessionBinding{ActiveWorkspace: binding.ActiveWorkspace, Workspaces: workspaces}
	}
	return cloned
}

func sameCodexSessionBindings(left map[string]codexSessionBinding, right map[string]codexSessionBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for key, binding := range left {
		rightBinding, ok := right[key]
		if !ok || !sameCodexSessionBinding(binding, rightBinding) {
			return false
		}
	}
	return true
}

func sameCodexSessionBinding(left codexSessionBinding, right codexSessionBinding) bool {
	if left.ActiveWorkspace != right.ActiveWorkspace || len(left.Workspaces) != len(right.Workspaces) {
		return false
	}
	for workspaceRoot, session := range left.Workspaces {
		if right.Workspaces[workspaceRoot] != session {
			return false
		}
	}
	return true
}

func remoteSelectionSnapshotLocked(bindings map[string]codexSessionBinding, bindingKey string, targetThreadID string) codexRemoteSelectionSnapshot {
	bindingKey = strings.TrimSpace(bindingKey)
	targetThreadID = strings.TrimSpace(targetThreadID)
	binding := cloneCodexSessionBindings(map[string]codexSessionBinding{
		bindingKey: bindings[bindingKey],
	})[bindingKey]
	return codexRemoteSelectionSnapshot{TargetThreadID: targetThreadID, Binding: binding}
}

// selectCodexRemoteWorkspace updates one frontend and removes duplicate thread
// references inside that same frontend only.
func selectCodexRemoteWorkspace(bindings map[string]codexSessionBinding, update codexRemoteSelectionUpdate, now time.Time) bool {
	binding := bindings[update.BindingKey]
	if binding.Workspaces == nil {
		binding.Workspaces = make(map[string]codexWorkspaceSession)
	}
	changed := binding.ActiveWorkspace != update.WorkspaceRoot
	binding.ActiveWorkspace = update.WorkspaceRoot
	for root, session := range binding.Workspaces {
		if root == update.WorkspaceRoot || strings.TrimSpace(session.ThreadID) != update.TargetThreadID {
			continue
		}
		session.ThreadID, session.PendingNewThread, session.PendingFirstTurn = "", false, false
		session.UpdatedAt = now.Format(time.RFC3339)
		binding.Workspaces[root], changed = session, true
	}
	target := binding.Workspaces[update.WorkspaceRoot]
	if strings.TrimSpace(target.ThreadID) != update.TargetThreadID || target.PendingNewThread {
		target.ThreadID, target.PendingNewThread = update.TargetThreadID, false
		target.PendingFirstTurn = update.PendingFirstTurn
		target.UpdatedAt, changed = now.Format(time.RFC3339), true
		binding.Workspaces[update.WorkspaceRoot] = target
	} else if update.PendingFirstTurn && !target.PendingFirstTurn {
		target.PendingFirstTurn = true
		target.UpdatedAt, changed = now.Format(time.RFC3339), true
		binding.Workspaces[update.WorkspaceRoot] = target
	}
	bindings[update.BindingKey] = binding
	return changed
}

func sortedUniqueCodexThreadIDs(threadIDs []string) []string {
	unique := make(map[string]struct{}, len(threadIDs))
	for _, threadID := range threadIDs {
		if threadID = strings.TrimSpace(threadID); threadID != "" {
			unique[threadID] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for threadID := range unique {
		result = append(result, threadID)
	}
	sort.Strings(result)
	return result
}
