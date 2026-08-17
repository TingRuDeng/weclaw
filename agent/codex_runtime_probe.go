package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	// Desktop history may initialize the IPC client, issue the history request,
	// and wait for the returned revision to be projected locally.
	codexRuntimeHandoffProbeTimeout = 2*codexDesktopRequestTimeout + codexDesktopStateApplyTimeout
	// Each blocking shared-host activation phase gets the former thread-control
	// RPC budget. The caller's shorter deadline still caps the complete handoff.
	codexRuntimeHandoffActivationTimeout = 15 * time.Second
	codexThreadTurnsPageSize             = 64
	codexThreadItemsPageSize             = 32
)

// InspectCodexRuntime 每次重新探测 Desktop，并同步已持久化的用户控制意图。
func (a *ACPAgent) InspectCodexRuntime(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	return a.inspectCodexRuntimeLocked(ctx, req)
}

func (a *ACPAgent) inspectCodexRuntimeLocked(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	if err := a.reconcileCodexHostTopologyLocked(ctx); err != nil {
		return unknownCodexRuntimeSnapshot(req, CodexThreadState{}), err
	}
	if a.desktopProbe == nil {
		return a.activateSharedCodexHost(ctx, req)
	}
	runtime, state, err := a.probeCodexRuntime(ctx, req, codexRuntimeProbeOptions{})
	if runtime == CodexRuntimeDesktop && a.codexDesktopHostSelection {
		if transitionErr := a.transitionCodexRuntimeToDesktop(ctx); transitionErr != nil {
			if a.desktopRuntime != nil {
				_ = a.desktopRuntime.disconnect()
			}
			return unknownCodexRuntimeSnapshot(req, state), transitionErr
		}
	}
	binding, activateErr := a.codexOwners.activateRuntime(req, runtime, state)
	if activateErr != nil {
		return binding, activateErr
	}
	return binding, err
}

// CurrentCodexRuntime 返回已建立的 runtime 绑定，不向 Desktop 发起同步探测。
func (a *ACPAgent) CurrentCodexRuntime(req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID)
	if !ok {
		return unknownCodexRuntimeSnapshot(req, CodexThreadState{}), nil
	}
	if a.desktopProbe == nil {
		binding.Ref = req.Ref
		binding.Control = req.Intent
		return binding, nil
	}
	if a.codexOwners.enforcesControl() && !sameCodexControlIntent(binding.Control, req.Intent) {
		if a.codexOwners.hasWriterLease(req.Ref.ThreadID) {
			return binding, ErrCodexControlChanged
		}
		return unknownCodexRuntimeSnapshot(req, binding.State), nil
	}
	binding.Ref = req.Ref
	return binding, nil
}

// ReconcileCodexObservedTurn 收敛显式接管后正在观察的 Desktop turn 状态。
func (a *ACPAgent) ReconcileCodexObservedTurn(_ context.Context, req CodexRuntimeRequest, state CodexThreadState) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	if a.desktopProbe == nil {
		binding, retained, err := a.codexOwners.reconcileUncertainSharedHostLease(req, state)
		if err != nil {
			return binding, err
		}
		if retained {
			return binding, nil
		}
		return a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, state)
	}
	return a.codexOwners.reconcileObservedTurn(req, state)
}

func unknownCodexRuntimeSnapshot(req CodexRuntimeRequest, state CodexThreadState) CodexThreadBinding {
	state.ThreadID = req.Ref.ThreadID
	return CodexThreadBinding{
		Ref: req.Ref, Control: req.Intent, Runtime: CodexRuntimeUnknown, State: state,
	}
}

// HandoffCodexRuntime 保留兼容 API 名称；它绑定 frontend，并只在确需更换 Host 时执行切换。
func (a *ACPAgent) HandoffCodexRuntime(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	return a.handoffCodexRuntimeLocked(ctx, req)
}

func (a *ACPAgent) handoffCodexRuntimeLocked(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	if req.Intent.Owner != CodexControlUnclaimed {
		if err := a.reconcileCodexHostTopologyLocked(ctx); err != nil {
			return CodexThreadBinding{}, err
		}
	}
	if a.desktopProbe == nil {
		return a.activateSharedCodexHostWithPhaseTimeout(
			ctx, req, codexRuntimeHandoffActivationTimeout,
		)
	}
	// A writer lease protects the accepted turn lifecycle, not a frontend route.
	// The production shared-host topology may bind another conversation to the
	// same runtime while that turn is active. Compatibility probes that still
	// enforce the retired owner model keep the old fail-closed behavior.
	if a.codexOwners.hasWriterLease(req.Ref.ThreadID) && a.codexOwners.enforcesControl() {
		return CodexThreadBinding{}, ErrCodexWriterBusy
	}
	if req.Intent.Owner == CodexControlUnclaimed {
		return a.codexOwners.activateRuntime(req, CodexRuntimeUnknown, CodexThreadState{ThreadID: req.Ref.ThreadID})
	}
	// thread/start/session resume 已经给出了当前 app-server 的本地 writer 证据。
	// 窗口认领只需同步控制 revision，不应为此再次探测 Codex Desktop。
	if req.Intent.Owner == CodexControlRemote && !a.codexDesktopCoordination {
		if current, ok := a.codexOwners.threadBinding(req.Ref.ThreadID); ok && current.Runtime == CodexRuntimeWeClaw {
			binding, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, current.State)
			if err == nil {
				a.bindCodexAppServerThread(req.Ref.ConversationID, req.Ref.ThreadID)
			}
			return binding, err
		}
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, codexRuntimeHandoffProbeTimeout)
	runtime, state, err := a.probeCodexRuntime(probeCtx, req, codexRuntimeProbeOptions{
		allowConflictRecovery: true,
		allowNoClientRelease:  true,
	})
	cancelProbe()
	if req.Intent.Owner == CodexControlRemote && canRecoverCodexRuntimeForRemoteOwner(err) &&
		(!a.codexDesktopCoordination || desktopHostDefinitelyAbsent(a.desktopProbe)) {
		log.Printf("[codex-runtime] remote owner 忽略 Desktop 探测不确定状态 thread=%q: %v", req.Ref.ThreadID, err)
		runtime, err = CodexRuntimeUnknown, nil
	}
	if err != nil && !(req.Intent.Owner == CodexControlDesktop && runtime == CodexRuntimeConflict) {
		return CodexThreadBinding{}, err
	}
	// The verified daemon is already the authoritative Host. Once the explicit
	// Desktop probe confirms release, attaching a new frontend only needs to
	// resume and read the target thread on the existing client. Restarting that
	// client would unnecessarily drain unrelated active turns.
	if req.Intent.Owner == CodexControlRemote && runtime == CodexRuntimeUnknown &&
		a.usesOfficialCodexDaemon() && a.codexRuntimeModeSnapshot() == CodexRuntimeWeClaw {
		return a.activateSharedCodexHostWithPhaseTimeout(
			ctx, req, codexRuntimeHandoffActivationTimeout,
		)
	}
	if req.Intent.Owner == CodexControlDesktop && runtime == CodexRuntimeConflict {
		runtime = CodexRuntimeDesktop
	}
	if runtime == CodexRuntimeDesktop && a.codexDesktopHostSelection {
		activationCtx, cancelActivation := context.WithTimeout(ctx, codexRuntimeHandoffActivationTimeout)
		defer cancelActivation()
		if transitionErr := a.transitionCodexRuntimeToDesktop(activationCtx); transitionErr != nil {
			if a.desktopRuntime != nil {
				_ = a.desktopRuntime.disconnect()
			}
			return CodexThreadBinding{}, transitionErr
		}
	}
	if req.Intent.Owner == CodexControlDesktop || runtime != CodexRuntimeUnknown {
		binding, activateErr := a.codexOwners.activateRuntime(req, runtime, state)
		if activateErr == nil && binding.Runtime == CodexRuntimeWeClaw {
			a.bindCodexAppServerThread(req.Ref.ConversationID, req.Ref.ThreadID)
		}
		return binding, activateErr
	}
	return a.recoverCodexRuntimeForRemoteWithPhaseTimeout(
		ctx, req, codexRuntimeHandoffActivationTimeout,
	)
}

// canRecoverCodexRuntimeForRemoteOwner 只放宽已持久化 remote owner 的 Desktop 探测结果。
// Desktop 不可达或旧 conflict 不能证明存在另一 writer；真正的 writer lease 仍在入口处拒绝移交。
func canRecoverCodexRuntimeForRemoteOwner(err error) bool {
	return errors.Is(err, ErrCodexDesktopOwnershipUnknown) || errors.Is(err, ErrCodexRuntimeConflict)
}

// MarkCodexRuntimeConflict 将无法确认 writer 的 thread 持续登记为冲突态。
func (a *ACPAgent) MarkCodexRuntimeConflict(ctx context.Context, req CodexRuntimeRequest) error {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return err
	}
	_ = ctx
	if a.desktopProbe == nil {
		// A transport timeout is not evidence of a second writer. The single
		// app-server remains authoritative and will reject conflicting turn IDs.
		return nil
	}
	_, err := a.codexOwners.markRuntimeConflict(req, "控制权移交结果未确认")
	return err
}

// activateSharedCodexHost binds a frontend route to the one authoritative
// app-server. Repeated calls reuse the live connection and do not perform any
// Desktop ownership probe.
func (a *ACPAgent) activateSharedCodexHost(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	return a.activateSharedCodexHostWithPhaseTimeout(ctx, req, 0)
}

func (a *ACPAgent) activateSharedCodexHostWithPhaseTimeout(ctx context.Context, req CodexRuntimeRequest, phaseTimeout time.Duration) (CodexThreadBinding, error) {
	hasLease, uncertainLease := a.codexOwners.writerLeaseStatus(req.Ref.ThreadID)
	if hasLease && !uncertainLease {
		binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID)
		if !ok {
			return CodexThreadBinding{}, ErrCodexWriterBusy
		}
		binding.Ref = req.Ref
		return binding, nil
	}
	startCtx, cancelStart := codexRuntimeActivationPhaseContext(ctx, phaseTimeout)
	err := a.ensureCodexAppServerStartedForTurn(startCtx, req.Ref.ConversationID)
	cancelStart()
	if err != nil {
		return CodexThreadBinding{}, err
	}
	a.mu.Lock()
	boundThread := strings.TrimSpace(a.threads[req.Ref.ConversationID])
	shouldResume := boundThread != strings.TrimSpace(req.Ref.ThreadID) || a.resumeOnFirstUse[req.Ref.ConversationID]
	a.mu.Unlock()
	if shouldResume {
		resumeCtx, cancelResume := codexRuntimeActivationPhaseContext(ctx, phaseTimeout)
		err := a.resumeThread(resumeCtx, req.Ref.ConversationID, req.Ref.ThreadID)
		cancelResume()
		if err != nil {
			return CodexThreadBinding{}, fmt.Errorf("恢复 Codex thread 失败: %w", err)
		}
		a.bindCodexAppServerThread(req.Ref.ConversationID, req.Ref.ThreadID)
	}
	readCtx, cancelRead := codexRuntimeActivationPhaseContext(ctx, phaseTimeout)
	state, _, err := a.readCodexAppServerThreadStateResult(readCtx, req.Ref.ThreadID)
	cancelRead()
	if err != nil {
		return CodexThreadBinding{}, err
	}
	if uncertainLease {
		binding, retained, reconcileErr := a.codexOwners.reconcileUncertainSharedHostLease(req, state)
		if reconcileErr != nil {
			return binding, reconcileErr
		}
		if retained {
			return binding, nil
		}
	}
	return a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, state)
}

func codexRuntimeActivationPhaseContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func (a *ACPAgent) validateCodexRuntimeSupport(req CodexRuntimeRequest) error {
	if a.protocol != protocolCodexAppServer || a.codexOwners == nil {
		return ErrCodexRuntimeUnavailable
	}
	return validateCodexRuntimeRequest(req)
}

type codexRuntimeProbeOptions struct {
	allowConflictRecovery bool
	allowNoClientRelease  bool
}

// probeCodexRuntime 只把完整 Desktop 快照或明确无人处理视为确定性结论。
func (a *ACPAgent) probeCodexRuntime(ctx context.Context, req CodexRuntimeRequest, opts codexRuntimeProbeOptions) (CodexRuntimeHolder, CodexThreadState, error) {
	current, _ := a.codexOwners.threadBinding(req.Ref.ThreadID)
	if a.desktopProbe == nil {
		return CodexRuntimeUnknown, current.State, ErrCodexDesktopOwnershipUnknown
	}
	if a.codexDesktopCoordination && desktopHostDefinitelyAbsent(a.desktopProbe) {
		if current.Runtime == CodexRuntimeWeClaw {
			return CodexRuntimeWeClaw, current.State, nil
		}
		return CodexRuntimeUnknown, current.State, nil
	}
	loadErr := a.desktopProbe.LoadHistory(ctx, req.Ref)
	if a.codexDesktopHostSelection && loadErr == nil && a.desktopRuntime != nil {
		if state, stateErr := a.desktopRuntime.threadState(req.Ref.ThreadID); stateErr == nil {
			return CodexRuntimeDesktop, state, nil
		}
	}
	// App 和 WeClaw 连接的是同一个 official daemon 时，App 历史只证明
	// thread 对该前端可见，不代表 Desktop 拥有另一个 Host。显式接管应继续
	// 通过现有 daemon 客户端 resume/read，并由该连接建立实时观察器。
	if loadErr == nil && opts.allowNoClientRelease && !a.codexDesktopHostSelection &&
		a.usesOfficialCodexDaemon() && a.codexRuntimeModeSnapshot() == CodexRuntimeWeClaw {
		return CodexRuntimeUnknown, current.State, nil
	}
	if binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID); ok {
		if binding.Runtime == CodexRuntimeConflict {
			if opts.allowConflictRecovery && desktopReleaseConfirmed(a.desktopProbe, loadErr) {
				return CodexRuntimeUnknown, binding.State, nil
			}
			return CodexRuntimeConflict, binding.State, ErrCodexRuntimeConflict
		}
		if a.codexDesktopHostSelection && binding.Runtime == CodexRuntimeDesktop {
			return CodexRuntimeDesktop, binding.State, nil
		}
	}
	released := desktopReleaseConfirmed(a.desktopProbe, loadErr)
	// A verified official daemon remains the only Host even when the App is
	// visible. During an explicit handoff, no-client then proves only that the
	// App frontend released this thread, so the shared daemon may resume it.
	officialDaemonHandoff := opts.allowNoClientRelease && a.usesOfficialCodexDaemon() &&
		a.codexRuntimeModeSnapshot() == CodexRuntimeWeClaw
	if a.codexDesktopCoordination && !officialDaemonHandoff {
		released = desktopHostDefinitelyAbsent(a.desktopProbe)
	}
	if released {
		if current.Runtime == CodexRuntimeWeClaw {
			return CodexRuntimeWeClaw, current.State, nil
		}
		return CodexRuntimeUnknown, current.State, nil
	}
	return CodexRuntimeUnknown, current.State, codexProbeError(loadErr)
}

func desktopReleaseConfirmed(probe codexDesktopOwnerProbe, loadErr error) bool {
	if errors.Is(loadErr, ErrCodexDesktopNoClient) {
		return true
	}
	socketExists, processExists := probe.Presence()
	return !socketExists && !processExists
}

func codexProbeError(loadErr error) error {
	if loadErr != nil {
		return fmt.Errorf("%w: %v", ErrCodexDesktopOwnershipUnknown, loadErr)
	}
	return ErrCodexDesktopOwnershipUnknown
}

func (a *ACPAgent) recoverCodexRuntimeForRemoteWithPhaseTimeout(ctx context.Context, req CodexRuntimeRequest, phaseTimeout time.Duration) (CodexThreadBinding, error) {
	restartCtx, cancelRestart := codexRuntimeActivationPhaseContext(ctx, phaseTimeout)
	err := a.restartCodexAppServer(restartCtx)
	cancelRestart()
	if err != nil {
		return CodexThreadBinding{}, err
	}
	if a.codexDesktopHostSelection && a.codexRuntimeModeSnapshot() != CodexRuntimeWeClaw {
		return CodexThreadBinding{}, ErrCodexRuntimeUnavailable
	}
	resumeCtx, cancelResume := codexRuntimeActivationPhaseContext(ctx, phaseTimeout)
	err = a.resumeThread(resumeCtx, req.Ref.ConversationID, req.Ref.ThreadID)
	cancelResume()
	if err != nil {
		return CodexThreadBinding{}, fmt.Errorf("恢复 Codex thread 失败: %w", err)
	}
	readCtx, cancelRead := codexRuntimeActivationPhaseContext(ctx, phaseTimeout)
	state, _, err := a.readCodexAppServerThreadStateResult(readCtx, req.Ref.ThreadID)
	cancelRead()
	if err != nil {
		return CodexThreadBinding{}, err
	}
	binding, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, state)
	if err == nil {
		a.bindCodexAppServerThread(req.Ref.ConversationID, req.Ref.ThreadID)
	}
	return binding, err
}

func (a *ACPAgent) bindCodexAppServerThread(conversationID string, threadID string) {
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID == "" || threadID == "" {
		return
	}
	a.mu.Lock()
	a.threads[conversationID] = threadID
	delete(a.resumeOnFirstUse, conversationID)
	a.mu.Unlock()
	a.persistState()
}

func (a *ACPAgent) readCodexAppServerThreadState(ctx context.Context, threadID string) (CodexThreadState, error) {
	state, _, err := a.readCodexAppServerThreadStateResult(ctx, threadID)
	return state, err
}

func (a *ACPAgent) readCodexAppServerThreadStateResult(ctx context.Context, threadID string) (CodexThreadState, bool, error) {
	thread, pendingFirstTurn, _, err := a.readCodexAppServerThreadMetadata(ctx, threadID)
	if err != nil || pendingFirstTurn {
		return codexThreadStateFromSnapshot(thread), pendingFirstTurn, err
	}
	turn, found, _, err := a.readCodexAppServerTargetTurn(ctx, threadID, "", false)
	if err != nil {
		return CodexThreadState{}, false, err
	}
	if found {
		thread.Turns = []codexTurnSnapshot{turn}
	}
	return codexThreadStateFromSnapshot(thread), false, nil
}

// readCodexAppServerThreadSnapshotResult 先读取轻量 thread 元数据，再只为目标
// turn 分页加载 items，避免把整个 rollout 历史放进一条 ACP 响应。
func (a *ACPAgent) readCodexAppServerThreadSnapshotResult(ctx context.Context, threadID string, targetTurnID string) (CodexThreadState, codexThreadSnapshot, bool, uint64, error) {
	thread, pendingFirstTurn, sequence, err := a.readCodexAppServerThreadMetadata(ctx, threadID)
	if err != nil || pendingFirstTurn {
		return codexThreadStateFromSnapshot(thread), thread, pendingFirstTurn, sequence, err
	}
	turn, found, turnSequence, err := a.readCodexAppServerTargetTurn(ctx, threadID, targetTurnID, true)
	if turnSequence > sequence {
		sequence = turnSequence
	}
	if err != nil {
		return CodexThreadState{}, codexThreadSnapshot{}, false, sequence, err
	}
	if found {
		thread.Turns = []codexTurnSnapshot{turn}
	}
	state := codexThreadStateFromSnapshot(thread)
	if strings.TrimSpace(targetTurnID) != "" && found {
		state.Active = turn.Status == "inProgress"
		state.WaitingOnApproval = state.Active && codexStatusHasFlag(thread.Status.ActiveFlags, "waitingOnApproval")
		state.WaitingOnUserInput = state.Active && codexStatusHasFlag(thread.Status.ActiveFlags, "waitingOnUserInput")
	}
	return state, thread, false, sequence, nil
}

func (a *ACPAgent) readCodexAppServerThreadMetadata(ctx context.Context, threadID string) (codexThreadSnapshot, bool, uint64, error) {
	threadID = strings.TrimSpace(threadID)
	result, sequence, err := a.rpcWithSequence(ctx, "thread/read", map[string]interface{}{
		"threadId": threadID, "includeTurns": false,
	})
	if err != nil {
		if isCodexThreadPendingFirstTurn(err) {
			return codexThreadSnapshot{ID: threadID}, true, sequence, nil
		}
		return codexThreadSnapshot{}, false, sequence, err
	}
	var response codexThreadReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return codexThreadSnapshot{}, false, sequence, fmt.Errorf("parse thread/read result: %w", err)
	}
	if strings.TrimSpace(response.Thread.ID) == "" {
		return codexThreadSnapshot{}, false, sequence, fmt.Errorf("thread/read returned empty thread id")
	}
	return response.Thread, false, sequence, nil
}

func (a *ACPAgent) readCodexAppServerTargetTurn(ctx context.Context, threadID string, targetTurnID string, loadItems bool) (codexTurnSnapshot, bool, uint64, error) {
	threadID = strings.TrimSpace(threadID)
	targetTurnID = strings.TrimSpace(targetTurnID)
	cursor := ""
	seenCursors := make(map[string]bool)
	var sequence uint64
	for {
		params := map[string]interface{}{
			"threadId": threadID, "limit": codexThreadTurnsPageSize,
			"sortDirection": "desc", "itemsView": "notLoaded",
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, pageSequence, err := a.rpcWithSequence(ctx, "thread/turns/list", params)
		if pageSequence > sequence {
			sequence = pageSequence
		}
		if err != nil {
			if isCodexThreadPendingFirstTurn(err) {
				return codexTurnSnapshot{}, false, sequence, nil
			}
			return codexTurnSnapshot{}, false, sequence, fmt.Errorf("list Codex thread turns: %w", err)
		}
		var page codexThreadTurnsListResponse
		if err := json.Unmarshal(result, &page); err != nil {
			return codexTurnSnapshot{}, false, sequence, fmt.Errorf("parse thread/turns/list result: %w", err)
		}
		for _, turn := range page.Data {
			if targetTurnID == "" || strings.TrimSpace(turn.ID) == targetTurnID {
				if loadItems {
					items, itemsSequence, err := a.readCodexAppServerTurnItems(ctx, threadID, turn.ID)
					if itemsSequence > sequence {
						sequence = itemsSequence
					}
					if err != nil {
						return codexTurnSnapshot{}, false, sequence, err
					}
					turn.Items = items
				}
				return turn, true, sequence, nil
			}
		}
		nextCursor := strings.TrimSpace(page.NextCursor)
		if targetTurnID == "" || nextCursor == "" {
			break
		}
		if seenCursors[nextCursor] {
			return codexTurnSnapshot{}, false, sequence, fmt.Errorf("thread/turns/list returned repeated cursor")
		}
		seenCursors[nextCursor] = true
		cursor = nextCursor
	}
	if targetTurnID != "" {
		return codexTurnSnapshot{}, false, sequence, fmt.Errorf("%w: target turn %s not found", ErrCodexControlChanged, targetTurnID)
	}
	return codexTurnSnapshot{}, false, sequence, nil
}

func (a *ACPAgent) readCodexAppServerTurnItems(ctx context.Context, threadID string, turnID string) ([]codexThreadItem, uint64, error) {
	cursor := ""
	seenCursors := make(map[string]bool)
	items := make([]codexThreadItem, 0)
	var sequence uint64
	for {
		params := map[string]interface{}{
			"threadId": strings.TrimSpace(threadID), "turnId": strings.TrimSpace(turnID),
			"limit": codexThreadItemsPageSize, "sortDirection": "asc",
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, pageSequence, err := a.rpcWithSequence(ctx, "thread/items/list", params)
		if pageSequence > sequence {
			sequence = pageSequence
		}
		if err != nil {
			return nil, sequence, fmt.Errorf("list Codex turn items: %w", err)
		}
		var page codexThreadItemsListResponse
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, sequence, fmt.Errorf("parse thread/items/list result: %w", err)
		}
		for _, entry := range page.Data {
			if strings.TrimSpace(entry.TurnID) != strings.TrimSpace(turnID) {
				return nil, sequence, fmt.Errorf("thread/items/list returned item for different turn")
			}
			items = append(items, entry.Item)
		}
		nextCursor := strings.TrimSpace(page.NextCursor)
		if nextCursor == "" {
			return items, sequence, nil
		}
		if seenCursors[nextCursor] {
			return nil, sequence, fmt.Errorf("thread/items/list returned repeated cursor")
		}
		seenCursors[nextCursor] = true
		cursor = nextCursor
	}
}
