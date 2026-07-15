package messaging

import (
	"context"
	"errors"
	"fmt"
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
}

// codexSessionAcquireResult 返回已提交的路由、运行时和观察状态。
type codexSessionAcquireResult struct {
	route           codexConversationRoute
	resolution      codexRuntimeResolution
	externalState   externalCodexTaskState
	externalActive  bool
	agentSessionErr error
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
	ctx          context.Context
	resyncCtx    context.Context
	liveAgent    agent.CodexLiveRuntimeAgent
	change       codexRuntimeIntentChange
	resyncIntent codexControlIntent
}

// codexSessionAcquirePlan 固化锁内快照和有序旧所有权释放集合。
type codexSessionAcquirePlan struct {
	request  codexSessionAcquireRequest
	snapshot codexRemoteSelectionSnapshot
	changes  []codexRuntimeIntentChange
}

// codexSessionAcquireCommit 汇总 store 提交后的收尾输入。
type codexSessionAcquireCommit struct {
	request     codexSessionAcquireRequest
	resolution  codexRuntimeResolution
	committed   codexRemoteSelectionResult
	prepared    preparedExternalCodexTask
	reservation externalCodexTaskReservation
}

// codexSessionAcquireRuntimeCommit 汇总已应用运行时副作用和提交输入。
type codexSessionAcquireRuntimeCommit struct {
	plan       codexSessionAcquirePlan
	liveAgent  agent.CodexLiveRuntimeAgent
	resolution codexRuntimeResolution
	applied    []codexRuntimeIntentChange
}

// codexSessionAcquireRollback 保存逆序补偿所需的完整上下文。
type codexSessionAcquireRollback struct {
	plan        codexSessionAcquirePlan
	liveAgent   agent.CodexLiveRuntimeAgent
	applied     []codexRuntimeIntentChange
	reservation externalCodexTaskReservation
	cause       error
}

// acquireCodexSessionWithBindingLocked 在外层 binding 锁内执行选择接管 saga。
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
	resolution, applied, err := h.applyCodexRuntimeIntentChanges(plan, liveAgent)
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackCodexAcquire(codexSessionAcquireRollback{
			plan: plan, liveAgent: liveAgent, applied: applied, cause: err,
		})
	}
	return h.commitCodexSessionAcquire(codexSessionAcquireRuntimeCommit{
		plan: plan, liveAgent: liveAgent, resolution: resolution, applied: applied,
	})
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

// commitCodexSessionAcquire 先预留观察槽，再只提交一次持久化状态。
func (h *Handler) commitCodexSessionAcquire(commit codexSessionAcquireRuntimeCommit) (codexSessionAcquireResult, error) {
	opts := externalCodexTaskOptionsFromAcquire(commit.plan.request)
	// 只有首次 runtime 本身确认 active，二次 inactive 才是合法终态证据。
	// rollout-only active 表示 Desktop 断联后的共享任务，仍必须启动只读观察。
	opts.runtimeInactiveAuthoritative = commit.resolution.Binding.State.Active
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackPreparedCodexAcquire(commit, externalCodexTaskReservation{}, err)
	}
	if commit.resolution.Binding.State.Active &&
		!prepared.confirmedInactive && (!prepared.active || !prepared.state.Controllable) {
		err = fmt.Errorf("活动 Desktop 任务尚不能由当前窗口控制")
		return codexSessionAcquireResult{}, h.rollbackPreparedCodexAcquire(commit, externalCodexTaskReservation{}, err)
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackPreparedCodexAcquire(commit, externalCodexTaskReservation{}, err)
	}
	committed, err := h.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey:     commit.plan.request.route.bindingKey,
		WorkspaceRoot:  commit.plan.request.route.workspaceRoot,
		TargetThreadID: commit.plan.request.route.threadID,
		ConversationID: commit.plan.request.route.conversationID,
		Expected:       commit.plan.snapshot,
	})
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackPreparedCodexAcquire(commit, reservation, err)
	}
	return h.finishCodexSessionAcquire(codexSessionAcquireCommit{
		request: commit.plan.request, resolution: commit.resolution, committed: committed,
		prepared: prepared, reservation: reservation,
	}), nil
}

// rollbackPreparedCodexAcquire 将提交阶段失败统一转换为完整补偿输入。
func (h *Handler) rollbackPreparedCodexAcquire(commit codexSessionAcquireRuntimeCommit, reservation externalCodexTaskReservation, cause error) error {
	return h.rollbackCodexAcquire(codexSessionAcquireRollback{
		plan: commit.plan, liveAgent: commit.liveAgent, applied: commit.applied,
		reservation: reservation, cause: cause,
	})
}

// renderCodexSessionAcquireFailure 将内部错误收敛为不泄露其他窗口身份的用户提示。
func renderCodexSessionAcquireFailure(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errCodexRemoteSelectionOtherRoute):
		return "其他远程窗口正在控制该会话，请原窗口先释放。"
	case errors.Is(err, errCodexSessionAcquireActiveOld):
		return "当前远程任务仍在执行，请等待完成或先发送 /stop。"
	case errors.Is(err, errCodexRemoteSelectionChanged):
		return "Codex 会话所有权已被并发修改，请重新查询后重试。"
	case errors.Is(err, errCodexSessionAcquireUncertain):
		return "Codex 控制权移交结果未确认，当前禁止继续写入。"
	case isCodexSessionControlTimeout(err):
		return "前一项会话操作仍在处理，本次选择未执行。"
	case errors.Is(err, errCodexSessionAcquireUnsupported):
		return "当前 Codex Agent 不支持选择即接管。"
	default:
		return "切换并接管 Codex 会话失败: " + err.Error()
	}
}
