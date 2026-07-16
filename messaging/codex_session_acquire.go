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
	errCodexSessionAcquireActiveOld   = errors.New("当前远程任务仍在执行")
	errCodexSessionAcquireUncertain   = errors.New("Codex 控制权移交结果未确认")
	errCodexSessionAcquireUnsupported = errors.New("当前 Codex Agent 不支持选择即接管")
)

// codexSessionAcquireRequest 汇总调用方已持有 binding 锁后的事务输入。
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
	// forceRuntimeHandoff 只用于用户显式要求重新接管运行通道的 owner remote。
	forceRuntimeHandoff bool
}

// codexSessionAcquireResult 返回已提交的路由、运行时和观察状态。
type codexSessionAcquireResult struct {
	route           codexConversationRoute
	resolution      codexRuntimeResolution
	externalState   externalCodexTaskState
	externalActive  bool
	agentSessionErr error
	runtimeErr      error
}

// codexRuntimeIntentChange 描述单个 thread 的可补偿控制意图变化。
type codexRuntimeIntentChange struct {
	threadID string
	route    codexConversationRoute
	before   codexControlIntent
	after    codexControlIntent
}

// codexRuntimeHandoffRequest 描述一次副作用调用及失败后的权威校准意图。
type codexRuntimeHandoffRequest struct {
	ctx       context.Context
	resyncCtx context.Context
	liveAgent agent.CodexLiveRuntimeAgent
	change    codexRuntimeIntentChange
}

// codexSessionAcquirePlan 固化锁内快照和有序旧所有权释放集合。
type codexSessionAcquirePlan struct {
	request  codexSessionAcquireRequest
	snapshot codexRemoteSelectionSnapshot
	changes  []codexRuntimeIntentChange
}

// acquireCodexSessionWithBindingLocked 在外层 binding 锁内先提交窗口所有权，再同步运行通道。
func (h *Handler) acquireCodexSessionWithBindingLocked(req codexSessionAcquireRequest) (codexSessionAcquireResult, error) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return codexSessionAcquireResult{}, errCodexSessionAcquireUnsupported
	}
	store := h.ensureCodexSessions()
	initial := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: req.ctx, command: "acquire", threadIDs: codexRemoteSelectionThreadIDs(initial),
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	defer unlock()
	locked := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	if !sameCodexRemoteSelectionLockSet(initial, locked) {
		return codexSessionAcquireResult{}, errCodexRemoteSelectionChanged
	}
	plan, err := h.buildCodexSessionAcquirePlan(req, locked)
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	h.bindConversationCwd(req.agent, req.route.conversationID, req.route.workspaceRoot)
	committed, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey:     req.route.bindingKey,
		WorkspaceRoot:  req.route.workspaceRoot,
		TargetThreadID: req.route.threadID,
		ConversationID: req.route.conversationID,
		Expected:       plan.snapshot,
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}

	result := h.finishCodexOwnerSelection(req)
	if result.agentSessionErr != nil {
		rollbackErr := store.rollbackRemoteSelection(committed)
		if rollbackErr != nil {
			return codexSessionAcquireResult{}, errors.Join(
				errCodexSessionAcquireUncertain, result.agentSessionErr, rollbackErr,
			)
		}
		return codexSessionAcquireResult{}, result.agentSessionErr
	}
	resolution, _, runtimeErr := h.applyCodexRuntimeIntentChanges(plan, liveAgent)
	result.resolution = codexAcquireResolutionAfterCommit(req.route, committed.Target, resolution)
	if readyErr := ensureCodexRuntimeReady(result.resolution, req.route); readyErr != nil {
		result.runtimeErr = errors.Join(runtimeErr, readyErr)
		return result, nil
	}
	if runtimeErr != nil {
		// 目标通道已经可写时，旧会话释放失败只影响后台清理，不能撤销新所有权。
		log.Printf("[codex-session-acquire] 旧运行通道清理失败 target=%q: %v", req.route.threadID, runtimeErr)
	}
	return h.attachCodexAcquireObserver(result, req, liveAgent)
}

// codexAcquireResolutionAfterCommit 确保结果始终展示已提交的权威控制意图。
func codexAcquireResolutionAfterCommit(route codexConversationRoute, intent codexControlIntent, resolution codexRuntimeResolution) codexRuntimeResolution {
	if strings.TrimSpace(resolution.Request.Ref.ThreadID) == "" {
		resolution.Request = agent.CodexRuntimeRequest{Ref: route.ref(route.threadID)}
		resolution.Live = true
	}
	resolution.Request.Intent = agentControlIntent(intent)
	if strings.TrimSpace(resolution.Binding.Ref.ThreadID) == "" {
		resolution.Binding = unknownCodexRuntimeBinding(resolution.Request)
	}
	resolution.Binding.Ref = resolution.Request.Ref
	resolution.Binding.Control = resolution.Request.Intent
	return resolution
}

// sameCodexRemoteSelectionLockSet 确认等待锁期间没有出现未纳入保护的新 thread。
func sameCodexRemoteSelectionLockSet(left codexRemoteSelectionSnapshot, right codexRemoteSelectionSnapshot) bool {
	leftIDs := codexRemoteSelectionThreadIDs(left)
	rightIDs := codexRemoteSelectionThreadIDs(right)
	if len(leftIDs) != len(rightIDs) {
		return false
	}
	for index := range leftIDs {
		if leftIDs[index] != rightIDs[index] {
			return false
		}
	}
	return true
}

// buildCodexSessionAcquirePlan 在任何运行时副作用前完成冲突和活动任务校验。
func (h *Handler) buildCodexSessionAcquirePlan(req codexSessionAcquireRequest, snapshot codexRemoteSelectionSnapshot) (codexSessionAcquirePlan, error) {
	if snapshot.Target.Owner == codexControlRemote && snapshot.Target.RouteBindingKey != req.route.bindingKey {
		return codexSessionAcquirePlan{}, errCodexRemoteSelectionOtherRoute
	}
	plan := codexSessionAcquirePlan{request: req, snapshot: snapshot}
	for _, threadID := range sortedUniqueCodexThreadIDs(codexRemoteSelectionThreadIDs(snapshot)) {
		before, owned := snapshot.RouteOwned[threadID]
		if !owned || threadID == req.route.threadID {
			continue
		}
		if _, active := h.activeCodexTaskConversation(threadID); active {
			return codexSessionAcquirePlan{}, errCodexSessionAcquireActiveOld
		}
		plan.changes = append(plan.changes, oldCodexRuntimeIntentChange(req, snapshot, threadID, before))
	}
	return plan, nil
}

// oldCodexRuntimeIntentChange 为旧 remote thread 构造可逆的归还变化。
func oldCodexRuntimeIntentChange(req codexSessionAcquireRequest, snapshot codexRemoteSelectionSnapshot, threadID string, before codexControlIntent) codexRuntimeIntentChange {
	workspace := codexWorkspaceForThread(snapshot.Binding, threadID)
	return codexRuntimeIntentChange{
		threadID: threadID,
		route: codexConversationRoute{
			bindingKey: req.route.bindingKey, workspaceRoot: workspace,
			conversationID: before.ConversationID, threadID: threadID,
		},
		before: before,
		after:  codexControlIntent{Owner: codexControlDesktop, Revision: before.Revision + 1},
	}
}

// codexWorkspaceForThread 从锁内 binding 快照定位旧 thread 的 workspace。
func codexWorkspaceForThread(binding codexSessionBinding, threadID string) string {
	for workspace, session := range binding.Workspaces {
		if strings.TrimSpace(session.ThreadID) == threadID {
			return workspace
		}
	}
	return ""
}

// externalCodexTaskOptionsFromAcquire 将事务路由转换为观察 reservation 输入。
func externalCodexTaskOptionsFromAcquire(req codexSessionAcquireRequest) externalCodexTaskOptions {
	taskContext := req.taskContext
	if taskContext == nil {
		taskContext = normalizeContext(req.ctx)
	}
	return externalCodexTaskOptions{
		ctx: taskContext, actorUserID: req.actorUserID,
		routeUserID: req.routeUserID, agentName: req.agentName,
		agent: req.agent, conversationID: req.route.conversationID,
		threadID: req.route.threadID, platform: req.platform,
		accountID: req.accountID, reply: req.reply,
	}
}

// attachCodexAcquireObserver 在目标运行通道已就绪后附加活动任务观察；失败只关闭运行通道。
func (h *Handler) attachCodexAcquireObserver(result codexSessionAcquireResult, req codexSessionAcquireRequest, liveAgent agent.CodexLiveRuntimeAgent) (codexSessionAcquireResult, error) {
	opts := externalCodexTaskOptionsFromAcquire(req)
	// 只有首次 runtime 本身确认 active，二次 inactive 才是合法终态证据。
	// rollout-only active 表示 Desktop 断联后的共享任务，仍必须启动只读观察。
	opts.runtimeInactiveAuthoritative = result.resolution.Binding.State.Active
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return h.failCodexAcquireRuntime(result, liveAgent, err), nil
	}
	if result.resolution.Binding.State.Active &&
		!prepared.confirmedInactive && (!prepared.active || !prepared.state.Controllable) {
		err = fmt.Errorf("活动 Desktop 任务尚不能由当前窗口控制")
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
	return result, nil
}

// failCodexAcquireRuntime 保留窗口所有权，并把无法安全写入的目标持续标记为冲突态。
func (h *Handler) failCodexAcquireRuntime(result codexSessionAcquireResult, liveAgent agent.CodexLiveRuntimeAgent, cause error) codexSessionAcquireResult {
	request := result.resolution.Request
	markErr := liveAgent.MarkCodexRuntimeConflict(context.Background(), request)
	binding, currentErr := liveAgent.CurrentCodexRuntime(request)
	if currentErr == nil {
		result.resolution.Binding = binding
	}
	result.runtimeErr = errors.Join(cause, markErr, currentErr)
	return result
}

// renderCodexSessionAcquireFailure 将内部错误收敛为不泄露其他窗口身份的用户提示。
func renderCodexSessionAcquireFailure(err error) string {
	if err == nil {
		return ""
	}
	log.Printf("[codex-session-acquire] 切换并接管失败: %v", err)
	switch {
	case errors.Is(err, errCodexRemoteSelectionOtherRoute):
		return "其他远程窗口正在控制该会话，请原窗口先释放。"
	case errors.Is(err, errCodexSessionAcquireActiveOld):
		return "当前远程任务仍在执行，请等待完成或先发送 /stop。"
	case errors.Is(err, errCodexRemoteSelectionChanged):
		return "Codex 会话所有权已被并发修改，请重新查询后重试。"
	case errors.Is(err, errCodexSessionAcquireUncertain):
		return "未切换到 Codex：目标会话的控制权移交结果未确认。当前窗口仍保持切换前的 Agent；在状态确认前不会向该 Codex 会话写入。"
	case isCodexSessionControlTimeout(err):
		return "前一项会话操作仍在处理，本次选择未执行。"
	case errors.Is(err, errCodexSessionAcquireUnsupported):
		return "当前 Codex Agent 不支持选择即接管。"
	default:
		return "切换并接管 Codex 会话失败，请重试。"
	}
}
