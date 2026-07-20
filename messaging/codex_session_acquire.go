package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

var (
	errCodexSessionAcquireActiveOld   = errors.New("当前会话任务仍在执行")
	errCodexSessionAcquireUncertain   = errors.New("Codex 会话绑定结果未确认")
	errCodexSessionAcquireUnsupported = errors.New("当前 Codex Agent 不支持共享 app-server 会话绑定")
)

// codexSessionAcquireRequest describes one frontend binding operation. The
// route identifies a client view; it is not a global writer owner.
type codexSessionAcquireRequest struct {
	ctx         context.Context
	actorUserID string
	routeUserID string
	agentName   string
	agent       agent.Agent
	route       codexConversationRoute
	platform    platform.PlatformName
	accountID   string
	reply       platform.Replier
	taskContext context.Context
	// pendingFirstTurn marks a thread created by thread/start that has not yet
	// accepted its first user turn.
	pendingFirstTurn bool
}

type codexSessionAcquireResult struct {
	route               codexConversationRoute
	resolution          codexRuntimeResolution
	externalState       externalCodexTaskState
	externalActive      bool
	agentSessionErr     error
	runtimeErr          error
	selectionChanged    bool
	progressReanchored  bool
	progressReanchorErr error
}

// acquireCodexSessionWithBindingLocked atomically commits one frontend's
// workspace/thread binding, then asks the shared app-server client to bind its
// conversation mapping to that thread. Other frontends are never released or
// invalidated.
func (h *Handler) acquireCodexSessionWithBindingLocked(req codexSessionAcquireRequest) (codexSessionAcquireResult, error) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return codexSessionAcquireResult{}, errCodexSessionAcquireUnsupported
	}
	if err := h.guardCodexThreadSwitch(req.route, req.route.threadID); err != nil {
		return codexSessionAcquireResult{}, err
	}

	store := h.ensureCodexSessions()
	initial := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: req.ctx, command: "bind", threadIDs: codexRemoteSelectionThreadIDs(initial),
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	defer unlock()

	locked := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	if !sameCodexRemoteSelectionSnapshot(initial, locked) {
		return codexSessionAcquireResult{}, errCodexRemoteSelectionChanged
	}
	h.bindConversationCwd(req.agent, req.route.conversationID, req.route.workspaceRoot)
	committed, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey:       req.route.bindingKey,
		WorkspaceRoot:    req.route.workspaceRoot,
		TargetThreadID:   req.route.threadID,
		ConversationID:   req.route.conversationID,
		PendingFirstTurn: req.pendingFirstTurn,
		Expected:         locked,
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}

	result := h.finishCodexFrontendBinding(req)
	if result.agentSessionErr != nil {
		rollbackErr := store.rollbackRemoteSelection(committed)
		if rollbackErr != nil {
			return codexSessionAcquireResult{}, errors.Join(
				errCodexSessionAcquireUncertain, result.agentSessionErr, rollbackErr,
			)
		}
		return codexSessionAcquireResult{}, result.agentSessionErr
	}
	storeSelectionChanged := !codexRemoteSelectionMatchesRoute(locked, req.route)
	result.selectionChanged = h.codexTaskCardSelectionChanged(
		req.route.bindingKey, req.route.conversationID, storeSelectionChanged,
	)

	result.resolution, result.runtimeErr = h.bindCodexSharedRuntime(req, liveAgent)
	if result.runtimeErr != nil {
		return result, nil
	}
	result, err = h.attachCodexAcquireObserver(result, req, liveAgent)
	if err == nil && result.runtimeErr == nil &&
		(result.progressReanchorErr == nil || result.progressReanchored) {
		h.commitCodexTaskCardFocus(req.route.bindingKey, req.route.conversationID)
	}
	return result, err
}

// finishCodexFrontendBinding switches only this message route's workspace and
// selected Agent. It does not change any app-server writer authority.
func (h *Handler) finishCodexFrontendBinding(request codexSessionAcquireRequest) codexSessionAcquireResult {
	agentSessionErr := h.ensureAgentSessions().Set(request.routeUserID, request.agentName)
	if agentSessionErr != nil {
		return codexSessionAcquireResult{route: request.route, agentSessionErr: agentSessionErr}
	}
	h.switchCodexWorkspaceForRoute(
		firstNonBlank(request.actorUserID, request.routeUserID),
		request.routeUserID, request.agentName,
		request.route.workspaceRoot, request.agent,
	)
	return codexSessionAcquireResult{route: request.route}
}

func externalCodexTaskOptionsFromAcquire(req codexSessionAcquireRequest) externalCodexTaskOptions {
	taskContext := req.taskContext
	if taskContext == nil {
		taskContext = normalizeContext(req.ctx)
	}
	return externalCodexTaskOptions{
		ctx: taskContext, actorUserID: req.actorUserID,
		routeUserID: req.routeUserID, agentName: req.agentName,
		agent: req.agent, conversationID: req.route.conversationID,
		threadID: req.route.threadID, workspaceRoot: req.route.workspaceRoot, platform: req.platform,
		accountID: req.accountID, reply: req.reply,
	}
}

// attachCodexAcquireObserver mirrors a turn already active in the shared host.
// Failure affects progress mirroring only; the frontend binding remains valid.
func (h *Handler) attachCodexAcquireObserver(result codexSessionAcquireResult, req codexSessionAcquireRequest, liveAgent agent.CodexLiveRuntimeAgent) (codexSessionAcquireResult, error) {
	opts := externalCodexTaskOptionsFromAcquire(req)
	opts.runtimeInactiveAuthoritative = result.resolution.Binding.State.Active
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	if prepared.state.Controllable && (prepared.active || result.resolution.Binding.State.Active) {
		binding, reconcileErr := liveAgent.ReconcileCodexObservedTurn(
			req.ctx, result.resolution.Request, prepared.state.CodexThreadState,
		)
		if reconcileErr != nil {
			return h.failCodexAcquireRuntime(result, liveAgent, reconcileErr), nil
		}
		result.resolution.Binding = binding
	}
	if result.resolution.Binding.State.Active &&
		!prepared.confirmedInactive && (!prepared.active || !prepared.state.Controllable) {
		err = fmt.Errorf("共享 app-server 的活动任务暂不能建立观察流")
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	observerReady := h.activateExternalCodexTaskReservation(reservation)
	if prepared.active && !observerReady {
		h.cancelExternalCodexTaskReservation(reservation)
		return h.failCodexAcquireRuntime(result, liveAgent, errExternalCodexTaskReservationConflict), nil
	}
	result.externalState = prepared.state
	result.externalActive = prepared.active && observerReady
	if result.selectionChanged && reservation.reused && observerReady {
		result.progressReanchored, result.progressReanchorErr = h.reanchorActiveCodexTask(
			req.ctx, reservation.task, req.reply,
		)
	}
	return result, nil
}

func codexRemoteSelectionMatchesRoute(snapshot codexRemoteSelectionSnapshot, route codexConversationRoute) bool {
	workspaceRoot := normalizeCodexWorkspaceRoot(route.workspaceRoot)
	if normalizeCodexWorkspaceRoot(snapshot.Binding.ActiveWorkspace) != workspaceRoot {
		return false
	}
	session := snapshot.Binding.Workspaces[workspaceRoot]
	return !session.PendingNewThread && strings.TrimSpace(session.ThreadID) == strings.TrimSpace(route.threadID)
}

// codexTaskCardSelectionChanged compares the target with the last session that
// completed frontend acquisition. Browsing a workspace may update the persisted
// selection before the user chooses a session, so that store alone cannot tell
// whether a running task card needs to be moved back to the message bottom.
func (h *Handler) codexTaskCardSelectionChanged(bindingKey string, conversationID string, fallback bool) bool {
	bindingKey = strings.TrimSpace(bindingKey)
	conversationID = strings.TrimSpace(conversationID)
	if bindingKey == "" || conversationID == "" {
		return fallback
	}
	h.codexTaskCardFocusMu.Lock()
	defer h.codexTaskCardFocusMu.Unlock()
	previous, tracked := h.codexTaskCardFocus[bindingKey]
	if !tracked {
		return fallback
	}
	return previous != conversationID
}

func (h *Handler) commitCodexTaskCardFocus(bindingKey string, conversationID string) {
	bindingKey = strings.TrimSpace(bindingKey)
	conversationID = strings.TrimSpace(conversationID)
	if bindingKey == "" || conversationID == "" {
		return
	}
	h.codexTaskCardFocusMu.Lock()
	defer h.codexTaskCardFocusMu.Unlock()
	if h.codexTaskCardFocus == nil {
		h.codexTaskCardFocus = make(map[string]string)
	}
	h.codexTaskCardFocus[bindingKey] = conversationID
}

func (h *Handler) reanchorActiveCodexTask(ctx context.Context, task *activeAgentTask, reply platform.Replier) (bool, error) {
	progress, latest, ok := task.progressReanchorSnapshot()
	if !ok {
		return false, nil
	}
	moved, err := progress.reanchor(ctx, reply, latest)
	if moved {
		task.mu.Lock()
		task.trace = traceWithReply(task.trace, progressReplier(reply))
		trace := task.trace
		task.mu.Unlock()
		h.recordTraceStage(trace, "task.card_reanchored", "running", "progress card moved to latest message position")
	}
	if err != nil {
		log.Printf("[codex-session-bind] 任务卡重锚失败 moved=%t: %v", moved, err)
	}
	return moved, err
}

func (h *Handler) failCodexAcquireRuntime(result codexSessionAcquireResult, liveAgent agent.CodexLiveRuntimeAgent, cause error) codexSessionAcquireResult {
	request := result.resolution.Request
	binding, currentErr := liveAgent.CurrentCodexRuntime(request)
	if currentErr == nil {
		result.resolution.Binding = binding
	}
	result.runtimeErr = errors.Join(cause, currentErr)
	return result
}

func renderCodexSessionAcquireFailure(err error) string {
	if err == nil {
		return ""
	}
	log.Printf("[codex-session-bind] 绑定失败: %v", err)
	switch {
	case errors.Is(err, errCodexSessionAcquireActiveOld):
		return "当前会话任务仍在执行，请等待完成或先发送 /stop。"
	case errors.Is(err, errCodexRemoteSelectionChanged):
		return "Codex 会话绑定已被并发修改，请重新查询后重试。"
	case errors.Is(err, errCodexSessionAcquireUncertain):
		return "未切换到 Codex：会话绑定结果未确认。当前窗口仍保持切换前的 Agent。"
	case isCodexSessionControlTimeout(err):
		return "前一项会话操作仍在处理，本次绑定未执行。"
	case errors.Is(err, errCodexSessionAcquireUnsupported):
		return "当前 Codex Agent 不支持共享 app-server 会话绑定。"
	default:
		return "绑定 Codex 会话失败，请重试。"
	}
}
