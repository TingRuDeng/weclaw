package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var errClaudeBindingSelectionChanged = errors.New("Claude 会话绑定状态已变化")

type claudeSessionStoreImage struct {
	Bindings map[string]claudeSessionBinding
}

type claudeBindingSelectionSnapshot struct {
	TargetSessionID string
	Binding         claudeSessionBinding
}

type claudeBindingSelectionUpdate struct {
	BindingKey      string
	WorkspaceRoot   string
	TargetSessionID string
	BindingStatus   claudeBindingStatus
	Expected        claudeBindingSelectionSnapshot
}

type claudeBindingReleaseUpdate struct {
	BindingKey    string
	WorkspaceRoot string
	KeepSelection bool
	Expected      claudeBindingSelectionSnapshot
}

type claudeBindingMutation struct {
	Before   claudeSessionStoreImage
	After    claudeSessionStoreImage
	Previous claudeSessionBinding
	Current  claudeSessionBinding
}

// bindingSelectionSnapshot returns the immutable state used by the selection
// CAS. It deliberately contains no cross-window owner: two routes may bind the
// same Claude session.
func (s *claudeSessionStore) bindingSelectionSnapshot(bindingKey string, targetSessionID string) claudeBindingSelectionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return claudeBindingSelectionSnapshotLocked(s.bindings, bindingKey, targetSessionID)
}

func claudeBindingSelectionSnapshotLocked(
	bindings map[string]claudeSessionBinding,
	bindingKey string,
	targetSessionID string,
) claudeBindingSelectionSnapshot {
	return claudeBindingSelectionSnapshot{
		TargetSessionID: strings.TrimSpace(targetSessionID),
		Binding:         bindings[strings.TrimSpace(bindingKey)],
	}
}

// commitBindingSelection atomically updates one frontend binding. It never
// rejects another frontend that already references the target session; the
// session writer lease is acquired only when a prompt starts.
func (s *claudeSessionStore) commitBindingSelection(update claudeBindingSelectionUpdate) (claudeBindingMutation, error) {
	update = normalizeClaudeBindingSelectionUpdate(update)
	if err := validateClaudeBindingSelectionUpdate(update); err != nil {
		return claudeBindingMutation{}, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := claudeBindingSelectionSnapshotLocked(s.bindings, update.BindingKey, update.TargetSessionID)
	if current != update.Expected {
		return claudeBindingMutation{}, errClaudeBindingSelectionChanged
	}

	before := newClaudeSessionStoreImage(s.bindings)
	nextBindings := cloneClaudeBindings(s.bindings)
	selectClaudeBinding(nextBindings, update, time.Now().UTC())
	after := newClaudeSessionStoreImage(nextBindings)
	mutation := claudeBindingMutation{
		Before: before, After: after,
		Previous: before.Bindings[update.BindingKey], Current: after.Bindings[update.BindingKey],
	}
	if sameClaudeSessionStoreImage(before, after) {
		return mutation, nil
	}
	if err := s.persistClaudeCandidate(nextBindings); err != nil {
		return claudeBindingMutation{}, fmt.Errorf("保存 Claude 会话绑定: %w", err)
	}
	s.bindings = nextBindings
	return mutation, nil
}

// commitBindingRelease changes only the calling frontend. Keeping a selection
// is now a no-op because there is no persistent local/remote ownership to
// release.
func (s *claudeSessionStore) commitBindingRelease(update claudeBindingReleaseUpdate) (claudeBindingMutation, error) {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeClaudeWorkspaceRoot(update.WorkspaceRoot)
	if update.BindingKey == "" || update.WorkspaceRoot == "" {
		return claudeBindingMutation{}, fmt.Errorf("Claude 绑定释放缺少必要路由字段")
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := claudeBindingSelectionSnapshotLocked(
		s.bindings, update.BindingKey, update.Expected.TargetSessionID,
	)
	if current != update.Expected {
		return claudeBindingMutation{}, errClaudeBindingSelectionChanged
	}

	before := newClaudeSessionStoreImage(s.bindings)
	nextBindings := cloneClaudeBindings(s.bindings)
	if !update.KeepSelection {
		previous := nextBindings[update.BindingKey]
		if previous.WorkspaceRoot != update.WorkspaceRoot || previous.SessionID != "" || previous.Status != claudeBindingUnbound {
			nextBindings[update.BindingKey] = nextClaudeBinding(
				previous, update.WorkspaceRoot, "", claudeBindingUnbound, time.Now().UTC(),
			)
		}
	}
	after := newClaudeSessionStoreImage(nextBindings)
	mutation := claudeBindingMutation{
		Before: before, After: after,
		Previous: before.Bindings[update.BindingKey], Current: after.Bindings[update.BindingKey],
	}
	if sameClaudeSessionStoreImage(before, after) {
		return mutation, nil
	}
	if err := s.persistClaudeCandidate(nextBindings); err != nil {
		return claudeBindingMutation{}, fmt.Errorf("保存 Claude 会话解绑: %w", err)
	}
	s.bindings = nextBindings
	return mutation, nil
}

// rollbackBindingMutation only compensates an unchanged after-image so an old
// failure cannot overwrite a newer frontend choice.
func (s *claudeSessionStore) rollbackBindingMutation(mutation claudeBindingMutation) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := newClaudeSessionStoreImage(s.bindings)
	if !sameClaudeSessionStoreImage(current, mutation.After) {
		return errClaudeBindingSelectionChanged
	}
	bindings := cloneClaudeBindings(mutation.Before.Bindings)
	if err := s.persistClaudeCandidate(bindings); err != nil {
		return fmt.Errorf("回滚 Claude 会话绑定: %w", err)
	}
	s.bindings = bindings
	return nil
}

func normalizeClaudeBindingSelectionUpdate(update claudeBindingSelectionUpdate) claudeBindingSelectionUpdate {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeClaudeWorkspaceRoot(update.WorkspaceRoot)
	update.TargetSessionID = strings.TrimSpace(update.TargetSessionID)
	if update.BindingStatus == "" {
		update.BindingStatus = claudeBindingReady
	}
	return update
}

func validateClaudeBindingSelectionUpdate(update claudeBindingSelectionUpdate) error {
	if update.BindingKey == "" || update.WorkspaceRoot == "" || update.TargetSessionID == "" {
		return fmt.Errorf("Claude 绑定提交缺少必要路由字段")
	}
	if update.BindingStatus != claudeBindingReady && update.BindingStatus != claudeBindingResumeFailed {
		return fmt.Errorf("Claude 绑定提交包含无效运行状态 %q", update.BindingStatus)
	}
	if update.Expected.TargetSessionID != update.TargetSessionID {
		return errClaudeBindingSelectionChanged
	}
	return nil
}

func selectClaudeBinding(bindings map[string]claudeSessionBinding, update claudeBindingSelectionUpdate, now time.Time) {
	current := bindings[update.BindingKey]
	if current.WorkspaceRoot == update.WorkspaceRoot && current.SessionID == update.TargetSessionID && current.Status == update.BindingStatus {
		return
	}
	bindings[update.BindingKey] = nextClaudeBinding(
		current, update.WorkspaceRoot, update.TargetSessionID, update.BindingStatus, now,
	)
}

func nextClaudeBinding(
	current claudeSessionBinding,
	workspaceRoot string,
	sessionID string,
	status claudeBindingStatus,
	now time.Time,
) claudeSessionBinding {
	revision := current.Revision + 1
	if revision == 0 {
		revision = 1
	}
	return claudeSessionBinding{
		WorkspaceRoot: workspaceRoot,
		SessionID:     sessionID,
		Status:        status,
		Revision:      revision,
		UpdatedAt:     now.Format(time.RFC3339Nano),
	}
}

func (s *claudeSessionStore) persistClaudeCandidate(bindings map[string]claudeSessionBinding) error {
	if s.persist == nil {
		return nil
	}
	return s.persist(newClaudeSessionState(bindings))
}

func newClaudeSessionStoreImage(bindings map[string]claudeSessionBinding) claudeSessionStoreImage {
	return claudeSessionStoreImage{Bindings: cloneClaudeBindings(bindings)}
}

func sameClaudeSessionStoreImage(left claudeSessionStoreImage, right claudeSessionStoreImage) bool {
	return sameClaudeBindingMap(left.Bindings, right.Bindings)
}

func sameClaudeBindingMap(left map[string]claudeSessionBinding, right map[string]claudeSessionBinding) bool {
	if len(left) != len(right) {
		return false
	}
	for key, binding := range left {
		rightBinding, ok := right[key]
		if !ok || rightBinding != binding {
			return false
		}
	}
	return true
}
