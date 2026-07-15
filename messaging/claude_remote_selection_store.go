package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errClaudeRemoteSelectionChanged    = errors.New("Claude 会话选择状态已变化")
	errClaudeRemoteSelectionOtherRoute = errors.New("Claude 会话由其他远程窗口控制")
)

type claudeSessionStoreImage struct {
	Bindings map[string]claudeSessionBinding
	Controls map[string]claudeControlIntent
}

type claudeRemoteSelectionSnapshot struct {
	TargetSessionID string
	Binding         claudeSessionBinding
	Target          claudeControlIntent
	RouteOwned      map[string]claudeControlIntent
}

type claudeRemoteSelectionUpdate struct {
	BindingKey      string
	WorkspaceRoot   string
	TargetSessionID string
	ConversationID  string
	Expected        claudeRemoteSelectionSnapshot
}

type claudeRemoteReleaseUpdate struct {
	BindingKey    string
	WorkspaceRoot string
	KeepSelection bool
	Expected      claudeRemoteSelectionSnapshot
}

type claudeRemoteMutation struct {
	Before   claudeSessionStoreImage
	After    claudeSessionStoreImage
	Target   claudeControlIntent
	Released map[string]claudeControlIntent
}

// remoteSelectionSnapshot 返回 route 绑定、目标控制意图和 route 全部远程所有权的不可变快照。
func (s *claudeSessionStore) remoteSelectionSnapshot(bindingKey string, targetSessionID string) claudeRemoteSelectionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return claudeRemoteSelectionSnapshotLocked(s.bindings, s.controls, bindingKey, targetSessionID)
}

func claudeRemoteSelectionSnapshotLocked(
	bindings map[string]claudeSessionBinding,
	controls map[string]claudeControlIntent,
	bindingKey string,
	targetSessionID string,
) claudeRemoteSelectionSnapshot {
	bindingKey = strings.TrimSpace(bindingKey)
	targetSessionID = strings.TrimSpace(targetSessionID)
	target, ok := controls[targetSessionID]
	if !ok {
		target = claudeControlIntent{Owner: claudeOwnerUnclaimed}
	}
	routeOwned := make(map[string]claudeControlIntent)
	for sessionID, intent := range controls {
		if intent.Owner == claudeOwnerRemote && intent.BindingKey == bindingKey {
			routeOwned[sessionID] = intent
		}
	}
	return claudeRemoteSelectionSnapshot{
		TargetSessionID: targetSessionID,
		Binding:         bindings[bindingKey],
		Target:          target,
		RouteOwned:      routeOwned,
	}
}

// commitRemoteSelection 原子切换 binding、释放旧 session 并认领目标 session。
func (s *claudeSessionStore) commitRemoteSelection(update claudeRemoteSelectionUpdate) (claudeRemoteMutation, error) {
	update = normalizeClaudeRemoteSelectionUpdate(update)
	if err := validateClaudeRemoteSelectionUpdate(update); err != nil {
		return claudeRemoteMutation{}, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := claudeRemoteSelectionSnapshotLocked(s.bindings, s.controls, update.BindingKey, update.TargetSessionID)
	if current.Target.Owner == claudeOwnerRemote && current.Target.BindingKey != update.BindingKey {
		return claudeRemoteMutation{}, errClaudeRemoteSelectionOtherRoute
	}
	if !sameClaudeRemoteSelectionSnapshot(current, update.Expected) {
		return claudeRemoteMutation{}, errClaudeRemoteSelectionChanged
	}

	before := newClaudeSessionStoreImage(s.bindings, s.controls)
	nextBindings := cloneClaudeBindings(s.bindings)
	nextControls := cloneClaudeControls(s.controls)
	now := time.Now().UTC()
	selectClaudeRemoteBinding(nextBindings, update, now)
	released := releaseClaudeRemoteControls(nextControls, update.BindingKey, update.TargetSessionID, now)
	target := claimClaudeRemoteControl(nextControls, update, now)
	after := newClaudeSessionStoreImage(nextBindings, nextControls)
	mutation := claudeRemoteMutation{Before: before, After: after, Target: target, Released: released}
	if sameClaudeSessionStoreImage(before, after) {
		return mutation, nil
	}
	if err := s.persistClaudeCandidate(nextBindings, nextControls); err != nil {
		return claudeRemoteMutation{}, fmt.Errorf("保存 Claude 会话选择: %w", err)
	}
	s.bindings = nextBindings
	s.controls = nextControls
	return mutation, nil
}

// commitRemoteRelease 原子释放 route 的全部远程所有权，并按需保留最近选择。
func (s *claudeSessionStore) commitRemoteRelease(update claudeRemoteReleaseUpdate) (claudeRemoteMutation, error) {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeClaudeWorkspaceRoot(update.WorkspaceRoot)
	if update.BindingKey == "" || update.WorkspaceRoot == "" {
		return claudeRemoteMutation{}, fmt.Errorf("Claude 释放提交缺少必要路由字段")
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := claudeRemoteSelectionSnapshotLocked(
		s.bindings, s.controls, update.BindingKey, update.Expected.TargetSessionID,
	)
	if !sameClaudeRemoteSelectionSnapshot(current, update.Expected) {
		return claudeRemoteMutation{}, errClaudeRemoteSelectionChanged
	}

	before := newClaudeSessionStoreImage(s.bindings, s.controls)
	nextBindings := cloneClaudeBindings(s.bindings)
	nextControls := cloneClaudeControls(s.controls)
	now := time.Now().UTC()
	if !update.KeepSelection {
		currentBinding := nextBindings[update.BindingKey]
		if currentBinding.WorkspaceRoot != update.WorkspaceRoot || currentBinding.SessionID != "" || currentBinding.Status != claudeBindingUnbound {
			nextBindings[update.BindingKey] = claudeSessionBinding{
				WorkspaceRoot: update.WorkspaceRoot,
				Status:        claudeBindingUnbound,
				UpdatedAt:     now.Format(time.RFC3339),
			}
		}
	}
	released := releaseClaudeRemoteControls(nextControls, update.BindingKey, "", now)
	after := newClaudeSessionStoreImage(nextBindings, nextControls)
	target := nextControls[strings.TrimSpace(update.Expected.TargetSessionID)]
	mutation := claudeRemoteMutation{Before: before, After: after, Target: target, Released: released}
	if sameClaudeSessionStoreImage(before, after) {
		return mutation, nil
	}
	if err := s.persistClaudeCandidate(nextBindings, nextControls); err != nil {
		return claudeRemoteMutation{}, fmt.Errorf("保存 Claude 会话释放: %w", err)
	}
	s.bindings = nextBindings
	s.controls = nextControls
	return mutation, nil
}

// rollbackRemoteMutation 只补偿仍与 mutation.After 完全一致的状态，避免覆盖并发新提交。
func (s *claudeSessionStore) rollbackRemoteMutation(mutation claudeRemoteMutation) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := newClaudeSessionStoreImage(s.bindings, s.controls)
	if !sameClaudeSessionStoreImage(current, mutation.After) {
		return errClaudeRemoteSelectionChanged
	}
	bindings := cloneClaudeBindings(mutation.Before.Bindings)
	controls := cloneClaudeControls(mutation.Before.Controls)
	if err := s.persistClaudeCandidate(bindings, controls); err != nil {
		return fmt.Errorf("回滚 Claude 会话选择: %w", err)
	}
	s.bindings = bindings
	s.controls = controls
	return nil
}

func normalizeClaudeRemoteSelectionUpdate(update claudeRemoteSelectionUpdate) claudeRemoteSelectionUpdate {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeClaudeWorkspaceRoot(update.WorkspaceRoot)
	update.TargetSessionID = strings.TrimSpace(update.TargetSessionID)
	update.ConversationID = strings.TrimSpace(update.ConversationID)
	return update
}

func validateClaudeRemoteSelectionUpdate(update claudeRemoteSelectionUpdate) error {
	if update.BindingKey == "" || update.WorkspaceRoot == "" || update.TargetSessionID == "" || update.ConversationID == "" {
		return fmt.Errorf("Claude 选择提交缺少必要路由字段")
	}
	if update.Expected.TargetSessionID != update.TargetSessionID {
		return errClaudeRemoteSelectionChanged
	}
	return nil
}

func selectClaudeRemoteBinding(bindings map[string]claudeSessionBinding, update claudeRemoteSelectionUpdate, now time.Time) {
	current := bindings[update.BindingKey]
	if current.WorkspaceRoot == update.WorkspaceRoot && current.SessionID == update.TargetSessionID && current.Status == claudeBindingReady {
		return
	}
	bindings[update.BindingKey] = claudeSessionBinding{
		WorkspaceRoot: update.WorkspaceRoot,
		SessionID:     update.TargetSessionID,
		Status:        claudeBindingReady,
		UpdatedAt:     now.Format(time.RFC3339),
	}
}

func releaseClaudeRemoteControls(
	controls map[string]claudeControlIntent,
	bindingKey string,
	keepSessionID string,
	now time.Time,
) map[string]claudeControlIntent {
	released := make(map[string]claudeControlIntent)
	for sessionID, intent := range controls {
		if intent.Owner != claudeOwnerRemote || intent.BindingKey != bindingKey || sessionID == keepSessionID {
			continue
		}
		next := claudeControlIntent{
			Owner: claudeOwnerLocal, Revision: intent.Revision + 1,
			UpdatedAt: now.Format(time.RFC3339Nano),
		}
		controls[sessionID] = next
		released[sessionID] = next
	}
	return released
}

func claimClaudeRemoteControl(
	controls map[string]claudeControlIntent,
	update claudeRemoteSelectionUpdate,
	now time.Time,
) claudeControlIntent {
	current, ok := controls[update.TargetSessionID]
	if !ok {
		current = claudeControlIntent{Owner: claudeOwnerUnclaimed}
	}
	if current.Owner == claudeOwnerRemote && current.BindingKey == update.BindingKey && current.ConversationID == update.ConversationID {
		return current
	}
	next := claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: update.BindingKey, ConversationID: update.ConversationID,
		Revision: current.Revision + 1, UpdatedAt: now.Format(time.RFC3339Nano),
	}
	controls[update.TargetSessionID] = next
	return next
}

func (s *claudeSessionStore) persistClaudeCandidate(
	bindings map[string]claudeSessionBinding,
	controls map[string]claudeControlIntent,
) error {
	if s.persist == nil {
		return nil
	}
	return s.persist(newClaudeSessionState(bindings, controls))
}

func newClaudeSessionStoreImage(
	bindings map[string]claudeSessionBinding,
	controls map[string]claudeControlIntent,
) claudeSessionStoreImage {
	return claudeSessionStoreImage{
		Bindings: cloneClaudeBindings(bindings),
		Controls: cloneClaudeControls(controls),
	}
}

func sameClaudeRemoteSelectionSnapshot(left claudeRemoteSelectionSnapshot, right claudeRemoteSelectionSnapshot) bool {
	return left.TargetSessionID == right.TargetSessionID &&
		left.Binding == right.Binding && left.Target == right.Target &&
		sameClaudeControlIntentMap(left.RouteOwned, right.RouteOwned)
}

func sameClaudeSessionStoreImage(left claudeSessionStoreImage, right claudeSessionStoreImage) bool {
	return sameClaudeBindingMap(left.Bindings, right.Bindings) &&
		sameClaudeControlIntentMap(left.Controls, right.Controls)
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

func sameClaudeControlIntentMap(left map[string]claudeControlIntent, right map[string]claudeControlIntent) bool {
	if len(left) != len(right) {
		return false
	}
	for sessionID, intent := range left {
		rightIntent, ok := right[sessionID]
		if !ok || rightIntent != intent {
			return false
		}
	}
	return true
}
