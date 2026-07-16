package messaging

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	errCodexRemoteSelectionChanged    = errors.New("Codex 会话选择状态已变化")
	errCodexRemoteSelectionOtherRoute = errors.New("Codex 会话由其他远程窗口控制")
)

type codexRemoteSelectionSnapshot struct {
	TargetThreadID string
	Binding        codexSessionBinding
	Target         codexControlIntent
	RouteOwned     map[string]codexControlIntent
}

type codexRemoteSelectionState struct {
	bindings map[string]codexSessionBinding
	controls map[string]codexControlIntent
}

type codexRemoteSelectionUpdate struct {
	BindingKey     string
	WorkspaceRoot  string
	TargetThreadID string
	ConversationID string
	Expected       codexRemoteSelectionSnapshot
}

type codexRemoteSelectionResult struct {
	Target   codexControlIntent
	Released map[string]codexControlIntent
	before   codexRemoteSelectionState
	after    codexRemoteSelectionState
}

// remoteSelectionSnapshot 返回绑定、目标意图和当前 route 全量所有权的深拷贝。
func (s *codexSessionStore) remoteSelectionSnapshot(bindingKey string, targetThreadID string) codexRemoteSelectionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return remoteSelectionSnapshotLocked(codexRemoteSelectionState{
		bindings: s.bindings, controls: s.controls,
	}, bindingKey, targetThreadID)
}

// codexRemoteSelectionThreadIDs 返回快照涉及的去重有序 thread 集合。
func codexRemoteSelectionThreadIDs(snapshot codexRemoteSelectionSnapshot) []string {
	ids := make([]string, 0, len(snapshot.RouteOwned)+1)
	for threadID := range snapshot.RouteOwned {
		ids = append(ids, threadID)
	}
	if target := strings.TrimSpace(snapshot.TargetThreadID); target != "" {
		ids = append(ids, target)
	}
	return sortedUniqueCodexThreadIDs(ids)
}

// commitRemoteSelection 先持久化候选副本，成功后才一次替换 live maps。
func (s *codexSessionStore) commitRemoteSelection(update codexRemoteSelectionUpdate) (codexRemoteSelectionResult, error) {
	update = normalizeCodexRemoteSelectionUpdate(update)
	if err := validateCodexRemoteSelectionUpdate(update); err != nil {
		return codexRemoteSelectionResult{}, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := codexRemoteSelectionState{bindings: s.bindings, controls: s.controls}
	if err := validateCodexRemoteSelectionSnapshotLocked(current, update); err != nil {
		return codexRemoteSelectionResult{}, err
	}
	before := cloneCodexRemoteSelectionState(current)
	nextBindings := cloneCodexSessionBindings(s.bindings)
	nextControls := cloneCodexControlIntents(s.controls)
	now := time.Now().UTC()
	candidate := codexRemoteSelectionState{bindings: nextBindings, controls: nextControls}
	result, changed := applyCodexRemoteSelection(candidate, update, now)
	result.before = before
	result.after = cloneCodexRemoteSelectionState(candidate)
	if !changed {
		return result, nil
	}
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings, Controls: nextControls,
		Updated: now.Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return codexRemoteSelectionResult{}, fmt.Errorf("保存 Codex 会话选择: %w", err)
	}
	s.bindings, s.controls = nextBindings, nextControls
	return result, nil
}

// rollbackRemoteSelection 只在 live 状态仍等于本次提交结果时恢复提交前快照。
func (s *codexSessionStore) rollbackRemoteSelection(result codexRemoteSelectionResult) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := codexRemoteSelectionState{bindings: s.bindings, controls: s.controls}
	if !sameCodexRemoteSelectionState(current, result.after) {
		return errCodexRemoteSelectionChanged
	}
	bindings := cloneCodexSessionBindings(result.before.bindings)
	controls := cloneCodexControlIntents(result.before.controls)
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: bindings, Controls: controls,
		Updated: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return fmt.Errorf("回滚 Codex 会话选择: %w", err)
	}
	s.bindings, s.controls = bindings, controls
	return nil
}

func cloneCodexRemoteSelectionState(state codexRemoteSelectionState) codexRemoteSelectionState {
	return codexRemoteSelectionState{
		bindings: cloneCodexSessionBindings(state.bindings),
		controls: cloneCodexControlIntents(state.controls),
	}
}

func sameCodexRemoteSelectionState(left codexRemoteSelectionState, right codexRemoteSelectionState) bool {
	if len(left.bindings) != len(right.bindings) || len(left.controls) != len(right.controls) {
		return false
	}
	for key, binding := range left.bindings {
		rightBinding, ok := right.bindings[key]
		if !ok || !sameCodexSessionBinding(binding, rightBinding) {
			return false
		}
	}
	return sameCodexControlIntentMap(left.controls, right.controls)
}

// normalizeCodexRemoteSelectionUpdate 统一选择提交的外部输入边界。
func normalizeCodexRemoteSelectionUpdate(update codexRemoteSelectionUpdate) codexRemoteSelectionUpdate {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeCodexWorkspaceRoot(update.WorkspaceRoot)
	update.TargetThreadID = strings.TrimSpace(update.TargetThreadID)
	update.ConversationID = strings.TrimSpace(update.ConversationID)
	return update
}

// validateCodexRemoteSelectionUpdate 拒绝缺少路由字段或目标不匹配的提交。
func validateCodexRemoteSelectionUpdate(update codexRemoteSelectionUpdate) error {
	if update.BindingKey == "" || update.WorkspaceRoot == "" ||
		update.TargetThreadID == "" || update.ConversationID == "" {
		return fmt.Errorf("Codex 选择提交缺少必要路由字段")
	}
	if update.Expected.TargetThreadID != update.TargetThreadID {
		return errCodexRemoteSelectionChanged
	}
	return nil
}

// validateCodexRemoteSelectionSnapshotLocked 在当前锁内执行 route 所有权和完整快照 CAS。
func validateCodexRemoteSelectionSnapshotLocked(state codexRemoteSelectionState, update codexRemoteSelectionUpdate) error {
	current := remoteSelectionSnapshotLocked(state, update.BindingKey, update.TargetThreadID)
	if current.Target.Owner == codexControlRemote && current.Target.RouteBindingKey != update.BindingKey {
		return errCodexRemoteSelectionOtherRoute
	}
	if !sameCodexRemoteSelectionSnapshot(current, update.Expected) {
		return errCodexRemoteSelectionChanged
	}
	return nil
}

// sameCodexRemoteSelectionSnapshot 比较 CAS 所需的全部选择与所有权状态。
func sameCodexRemoteSelectionSnapshot(left codexRemoteSelectionSnapshot, right codexRemoteSelectionSnapshot) bool {
	return left.TargetThreadID == right.TargetThreadID &&
		sameCodexSessionBinding(left.Binding, right.Binding) &&
		left.Target == right.Target &&
		sameCodexControlIntentMap(left.RouteOwned, right.RouteOwned)
}

// cloneCodexSessionBindings 深拷贝 binding 及其 workspace map。
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

// cloneCodexControlIntents 复制控制意图 map，隔离候选状态和 live 状态。
func cloneCodexControlIntents(source map[string]codexControlIntent) map[string]codexControlIntent {
	cloned := make(map[string]codexControlIntent, len(source))
	for threadID, intent := range source {
		cloned[threadID] = intent
	}
	return cloned
}

// sameCodexSessionBinding 比较 binding 的 active 和完整 workspace 集合。
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

// sameCodexControlIntentMap 比较 route 全量所有权及 revision。
func sameCodexControlIntentMap(left map[string]codexControlIntent, right map[string]codexControlIntent) bool {
	if len(left) != len(right) {
		return false
	}
	for threadID, intent := range left {
		rightIntent, ok := right[threadID]
		if !ok || rightIntent != intent {
			return false
		}
	}
	return true
}

// remoteSelectionSnapshotLocked 在调用方持锁时构造不可变选择快照。
func remoteSelectionSnapshotLocked(state codexRemoteSelectionState, bindingKey string, targetThreadID string) codexRemoteSelectionSnapshot {
	bindingKey = strings.TrimSpace(bindingKey)
	targetThreadID = strings.TrimSpace(targetThreadID)
	binding := cloneCodexSessionBindings(map[string]codexSessionBinding{
		bindingKey: state.bindings[bindingKey],
	})[bindingKey]
	target, ok := state.controls[targetThreadID]
	if !ok {
		target = codexControlIntent{Owner: codexControlUnclaimed}
	}
	routeOwned := make(map[string]codexControlIntent)
	for threadID, intent := range state.controls {
		if intent.Owner == codexControlRemote && intent.RouteBindingKey == bindingKey {
			routeOwned[threadID] = intent
		}
	}
	return codexRemoteSelectionSnapshot{
		TargetThreadID: targetThreadID, Binding: binding, Target: target, RouteOwned: routeOwned,
	}
}

// applyCodexRemoteSelection 依序更新 binding、释放旧所有权并认领目标。
func applyCodexRemoteSelection(state codexRemoteSelectionState, update codexRemoteSelectionUpdate, now time.Time) (codexRemoteSelectionResult, bool) {
	bindingChanged := selectCodexRemoteWorkspace(state, update, now)
	released, releaseChanged := releaseCodexRemoteRouteThreads(state, update, now)
	target, targetChanged := claimCodexRemoteTarget(state, update, now)
	return codexRemoteSelectionResult{Target: target, Released: released},
		bindingChanged || releaseChanged || targetChanged
}

// selectCodexRemoteWorkspace 选中目标 workspace，并清除同 binding 内重复的目标 thread。
func selectCodexRemoteWorkspace(state codexRemoteSelectionState, update codexRemoteSelectionUpdate, now time.Time) bool {
	binding := state.bindings[update.BindingKey]
	if binding.Workspaces == nil {
		binding.Workspaces = make(map[string]codexWorkspaceSession)
	}
	changed := binding.ActiveWorkspace != update.WorkspaceRoot
	binding.ActiveWorkspace = update.WorkspaceRoot
	for root, session := range binding.Workspaces {
		if root == update.WorkspaceRoot || strings.TrimSpace(session.ThreadID) != update.TargetThreadID {
			continue
		}
		session.ThreadID, session.PendingNewThread = "", false
		session.UpdatedAt = now.Format(time.RFC3339)
		binding.Workspaces[root], changed = session, true
	}
	target := binding.Workspaces[update.WorkspaceRoot]
	if strings.TrimSpace(target.ThreadID) != update.TargetThreadID || target.PendingNewThread {
		target.ThreadID, target.PendingNewThread = update.TargetThreadID, false
		target.UpdatedAt, changed = now.Format(time.RFC3339), true
		binding.Workspaces[update.WorkspaceRoot] = target
	}
	state.bindings[update.BindingKey] = binding
	return changed
}

// releaseCodexRemoteRouteThreads 将当前 route 除目标外的全部 thread 交还桌面端。
func releaseCodexRemoteRouteThreads(state codexRemoteSelectionState, update codexRemoteSelectionUpdate, now time.Time) (map[string]codexControlIntent, bool) {
	released := make(map[string]codexControlIntent)
	for threadID, intent := range update.Expected.RouteOwned {
		if threadID == update.TargetThreadID {
			continue
		}
		next := codexControlIntent{
			Owner: codexControlDesktop, Revision: intent.Revision + 1,
			UpdatedAt: now.Format(time.RFC3339Nano),
		}
		state.controls[threadID], released[threadID] = next, next
	}
	return released, len(released) > 0
}

// claimCodexRemoteTarget 仅在控制三元组变化时增加目标 revision。
func claimCodexRemoteTarget(state codexRemoteSelectionState, update codexRemoteSelectionUpdate, now time.Time) (codexControlIntent, bool) {
	current, ok := state.controls[update.TargetThreadID]
	if !ok {
		current = codexControlIntent{Owner: codexControlUnclaimed}
	}
	if current.Owner == codexControlRemote && current.RouteBindingKey == update.BindingKey &&
		current.ConversationID == update.ConversationID {
		return current, false
	}
	next := codexControlIntent{
		Owner: codexControlRemote, RouteBindingKey: update.BindingKey,
		ConversationID: update.ConversationID, Revision: current.Revision + 1,
		UpdatedAt: now.Format(time.RFC3339Nano),
	}
	state.controls[update.TargetThreadID] = next
	return next, true
}

// sortedUniqueCodexThreadIDs 清洗、去重并稳定排序 thread ID。
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
