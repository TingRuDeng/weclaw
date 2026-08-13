package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	codexDesktopStateApplyTimeout = 10 * time.Second
	codexDesktopLocalHostID       = "local"
)

type codexDesktopRuntime struct {
	mu             sync.Mutex
	client         *codexDesktopClient
	state          *codexDesktopStateStore
	actions        *codexDesktopActions
	owners         *codexRuntimeOwnerRegistry
	presence       func() (bool, bool)
	authoritative  func() bool
	onDisconnect   func()
	onEvents       func(string, []*codexTurnEvent)
	refreshHistory func(context.Context, CodexThreadRef) error
	tracked        map[string]bool
}

func (r *codexDesktopRuntime) approvalRequestState(ctx context.Context, threadID string, turnID string, requestID string) (ApprovalRequestState, error) {
	threadID = strings.TrimSpace(threadID)
	r.mu.Lock()
	refresh := r.refreshHistory
	r.mu.Unlock()
	if refresh == nil {
		refresh = r.LoadHistory
	}
	if err := refresh(ctx, CodexThreadRef{ThreadID: threadID}); err != nil {
		return ApprovalRequestStateUnknown, err
	}
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state == nil {
		return ApprovalRequestStateUnknown, ErrCodexDesktopUnavailable
	}
	snapshot, ok := state.snapshot(threadID)
	if !ok {
		return ApprovalRequestStateUnknown, ErrCodexDesktopOwnershipUnknown
	}
	return codexDesktopApprovalRequestState(snapshot, turnID, requestID), nil
}

type codexDesktopFollowingStatus struct {
	ConversationID string
	HostID         string
	TargetClientID string
}

// newCodexDesktopRuntime 创建尚未连接 socket 的懒初始化 runtime。
func newCodexDesktopRuntime() *codexDesktopRuntime {
	return &codexDesktopRuntime{
		presence: codexDesktopPresence,
		tracked:  make(map[string]bool),
	}
}

// setOwnerRegistry 建立 snapshot 到 owner registry 的单向通知。
func (r *codexDesktopRuntime) setOwnerRegistry(owners *codexRuntimeOwnerRegistry) {
	r.mu.Lock()
	r.owners = owners
	r.mu.Unlock()
}

// setAuthoritative 决定 Desktop 广播当前是否可更新 Host 级 runtime 权威。
func (r *codexDesktopRuntime) setAuthoritative(authoritative func() bool) {
	r.mu.Lock()
	r.authoritative = authoritative
	r.mu.Unlock()
}

// setDisconnectHandler 在 IPC 丢失后同步 Host 级 runtime 模式。
func (r *codexDesktopRuntime) setDisconnectHandler(handler func()) {
	r.mu.Lock()
	r.onDisconnect = handler
	r.mu.Unlock()
}

func (r *codexDesktopRuntime) connect(ctx context.Context) error {
	return r.ensureInitialized().Connect(ctx)
}

func (r *codexDesktopRuntime) disconnect() error {
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Disconnect()
}

// setEventHandler 注入 ACPAgent 的统一 turn event 分发器。
func (r *codexDesktopRuntime) setEventHandler(handler func(string, []*codexTurnEvent)) {
	r.mu.Lock()
	r.onEvents = handler
	r.mu.Unlock()
}

// threadState 返回 Desktop projector 的最新不可变状态。
func (r *codexDesktopRuntime) threadState(threadID string) (CodexThreadState, error) {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state == nil {
		return CodexThreadState{}, ErrCodexDesktopUnavailable
	}
	snapshot, ok := state.snapshot(threadID)
	if !ok {
		return CodexThreadState{}, ErrCodexDesktopOwnershipUnknown
	}
	return snapshot.State, nil
}

// replayActiveTurnEvents 在 watcher 先完成注册后，从带 revision
// 屏障的当前快照回放可见进度和尚未取得的交互。
func (r *codexDesktopRuntime) replayActiveTurnEvents(threadID string) []*codexTurnEvent {
	return r.replayActiveTurnBatch(threadID).Events
}

func (r *codexDesktopRuntime) replayActiveTurnBatch(threadID string) codexDesktopReplayBatch {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state == nil {
		return codexDesktopReplayBatch{}
	}
	return state.replayActiveTurnBatch(threadID)
}

func (r *codexDesktopRuntime) activeWatchSnapshot(threadID string) (CodexThreadState, codexDesktopReplayBatch, error) {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state == nil {
		return CodexThreadState{}, codexDesktopReplayBatch{}, ErrCodexDesktopUnavailable
	}
	threadState, batch, ok := state.activeWatchSnapshot(threadID)
	if !ok {
		return CodexThreadState{}, codexDesktopReplayBatch{}, ErrCodexDesktopOwnershipUnknown
	}
	return threadState, batch, nil
}

func (r *codexDesktopRuntime) awaitingFinalAnswer(threadID string, turnID string) bool {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	return state != nil && state.awaitingFinalAnswer(threadID, turnID)
}

func (r *codexDesktopRuntime) abandonTurnEvent(threadID string, event *codexTurnEvent) {
	if event == nil {
		return
	}
	requestID := ""
	if event.Approval != nil {
		requestID = event.Approval.Request.RequestID
	} else if event.UserInput != nil {
		requestID = event.UserInput.Request.RequestID
	}
	if strings.TrimSpace(requestID) == "" {
		return
	}
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state != nil {
		state.resetPendingActionOnError(threadID, requestID, context.Canceled)
	}
}

func (r *codexDesktopRuntime) replayPendingActionEvents(threadID string) []*codexTurnEvent {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state == nil {
		return nil
	}
	return state.replayPendingActionEvents(threadID)
}

// startTurn 通过 follower 在同一个 Desktop thread 开始任务。
func (r *codexDesktopRuntime) startTurn(ctx context.Context, spec codexDesktopStartTurnSpec) (string, error) {
	r.mu.Lock()
	actions := r.actions
	r.mu.Unlock()
	if actions == nil {
		return "", ErrCodexDesktopUnavailable
	}
	return actions.startTurn(ctx, spec)
}

// steerTurn 通过 follower 引导 Desktop active turn。
func (r *codexDesktopRuntime) steerTurn(ctx context.Context, spec codexDesktopSteerTurnSpec) error {
	r.mu.Lock()
	actions := r.actions
	r.mu.Unlock()
	if actions == nil {
		return ErrCodexDesktopUnavailable
	}
	return actions.steerTurn(ctx, spec)
}

// interruptTurn 通过 follower 停止 Desktop active turn。
func (r *codexDesktopRuntime) interruptTurn(ctx context.Context, threadID string, turnID string) error {
	r.mu.Lock()
	actions := r.actions
	r.mu.Unlock()
	if actions == nil {
		return ErrCodexDesktopUnavailable
	}
	return actions.interruptTurn(ctx, threadID, turnID)
}

func (r *codexDesktopRuntime) updateThreadSettings(ctx context.Context, threadID string, settings map[string]any) error {
	r.mu.Lock()
	actions := r.actions
	r.mu.Unlock()
	if actions == nil {
		return ErrCodexDesktopUnavailable
	}
	return actions.updateThreadSettings(ctx, threadID, settings)
}

// ensureInitialized 首次使用时才创建 IPC client、actions 和 state store。
func (r *codexDesktopRuntime) ensureInitialized() *codexDesktopClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client
	}
	client := newCodexDesktopClient(codexDesktopClientOptions{
		onBroadcast:  r.handleBroadcast,
		onDisconnect: r.handleDisconnect,
	})
	actions := newCodexDesktopActions(client, client.nextRequestID)
	state := newCodexDesktopStateStore(codexDesktopStateOptions{
		actions:            actions,
		approvalStateProbe: r.approvalRequestState,
		requestSnapshot: func(threadID string) {
			ref := CodexThreadRef{ThreadID: threadID}
			if err := r.requestHistory(context.Background(), ref, false); err != nil {
				log.Printf("[acp] Desktop snapshot recovery failed (thread=%s): %v", threadID, err)
			}
		},
	})
	r.client, r.actions, r.state = client, actions, state
	return client
}

// handleDisconnect 只降级实际运行位置；持久化远程控制方继续保持不变。
func (r *codexDesktopRuntime) handleDisconnect(cause error) {
	r.mu.Lock()
	owners, onDisconnect := r.owners, r.onDisconnect
	r.mu.Unlock()
	if owners != nil {
		owners.markDesktopDisconnected()
	}
	if onDisconnect != nil {
		onDisconnect()
	}
	log.Printf("[acp] Codex Desktop IPC disconnected; cached runtime marked unknown, control owner unchanged: %v", cause)
}

// LoadHistory 请求 Desktop 广播目标 thread 的完整 conversation state。
func (r *codexDesktopRuntime) LoadHistory(ctx context.Context, ref CodexThreadRef) error {
	return r.requestHistory(ctx, ref, true)
}

// requestHistory 请求目标完整状态，并按需等待返回 revision 完成投影。
func (r *codexDesktopRuntime) requestHistory(ctx context.Context, ref CodexThreadRef, wait bool) error {
	r.trackThread(ref.ThreadID)
	client := r.ensureInitialized()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	if err := r.announceFollowing(ctx, client, ref.ThreadID); err != nil {
		return err
	}
	result, err := client.Call(ctx, "thread-follower-load-complete-history", map[string]string{
		"conversationId": ref.ThreadID,
	})
	if err != nil || !wait {
		return err
	}
	revision, err := codexDesktopLoadRevision(result)
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, codexDesktopStateApplyTimeout)
	defer cancel()
	return r.state.waitForRevision(waitCtx, strings.TrimSpace(ref.ThreadID), client.Epoch(), revision)
}

// announceFollowing 把显式跟踪从一次性状态询问改成幂等的当前状态声明。
// App 已先打开 thread 时不会再次询问，只有主动登记后 owner 才能回放 snapshot。
func (r *codexDesktopRuntime) announceFollowing(ctx context.Context, client *codexDesktopClient, threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if client == nil || threadID == "" {
		return ErrCodexDesktopUnavailable
	}
	r.mu.Lock()
	authoritative := r.authoritative
	state := r.state
	r.mu.Unlock()
	if authoritative == nil || !authoritative() {
		return nil
	}
	epoch := client.Epoch()
	hasCurrentSnapshot := false
	if state != nil {
		if snapshot, ok := state.snapshot(threadID); ok && snapshot.ConnectionEpoch == epoch {
			hasCurrentSnapshot = true
		}
	}
	if err := client.broadcastForEpoch(ctx, epoch, "thread-stream-following-changed", map[string]any{
		"conversationId": threadID,
		"hostId":         codexDesktopLocalHostID,
		"following":      true,
	}, nil); err != nil {
		return err
	}
	if state == nil || hasCurrentSnapshot {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, codexDesktopStateApplyTimeout)
	defer cancel()
	return state.waitForRevision(waitCtx, threadID, epoch, 0)
}

// codexDesktopLoadRevision 提取 Desktop load-complete-history 的状态屏障 revision。
func codexDesktopLoadRevision(result json.RawMessage) (uint64, error) {
	var response struct {
		Revision uint64 `json:"revision"`
	}
	if json.Unmarshal(result, &response) != nil || response.Revision == 0 {
		return 0, fmt.Errorf("Codex Desktop history 响应缺少有效 revision")
	}
	return response.Revision, nil
}

// trackThread 标记 WeClaw 明确接管的 Desktop thread。
func (r *codexDesktopRuntime) trackThread(threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	r.mu.Lock()
	if r.tracked == nil {
		r.tracked = make(map[string]bool)
	}
	r.tracked[threadID] = true
	r.mu.Unlock()
}

// Presence 返回 socket 与 Codex 主进程存在性。
func (r *codexDesktopRuntime) Presence() (bool, bool) {
	r.mu.Lock()
	presence := r.presence
	r.mu.Unlock()
	if presence == nil {
		return codexDesktopPresence()
	}
	return presence()
}

// handleBroadcast 把状态广播投影到 owner registry 和统一 turn events。
// sourceEpoch 必须随广播跨过异步队列，旧连接事件不能借用当前连接代次。
func (r *codexDesktopRuntime) handleBroadcast(sourceEpoch uint64, envelope codexDesktopEnvelope) {
	r.mu.Lock()
	client, state, owners, authoritative, onEvents := r.client, r.state, r.owners, r.authoritative, r.onEvents
	tracked := r.tracked[codexDesktopBroadcastThreadID(envelope)]
	r.mu.Unlock()
	if client == nil || client.Epoch() != sourceEpoch {
		return
	}
	if envelope.Method == "thread-stream-following-status-requested" {
		r.answerFollowingStatusRequest(sourceEpoch, envelope, client, authoritative, tracked)
		return
	}
	if state == nil {
		return
	}
	if envelope.Method == "thread-stream-state-changed" && !tracked {
		return
	}
	update, err := state.applyEnvelope(sourceEpoch, envelope)
	if err != nil {
		log.Printf("[acp] Desktop state projection failed: %v", err)
		return
	}
	// Publish while the client holds its epoch lock so installConnection cannot
	// advance the generation between validation and owner/task notification.
	client.publishForEpoch(sourceEpoch, func() {
		isAuthoritative := authoritative == nil || authoritative()
		if owners != nil && update.Applied && isAuthoritative {
			owners.observeDesktopSnapshot(update.Snapshot.ThreadID, update.Snapshot.Revision, update.Snapshot.State)
		}
		if onEvents != nil && len(update.Events) > 0 && isAuthoritative {
			onEvents(update.Snapshot.ThreadID, update.Events)
		}
	})
}

func (r *codexDesktopRuntime) answerFollowingStatusRequest(
	sourceEpoch uint64,
	envelope codexDesktopEnvelope,
	client *codexDesktopClient,
	authoritative func() bool,
	tracked bool,
) {
	isAuthoritative := authoritative != nil && authoritative()
	status, ok := codexDesktopFollowingStatusResponse(envelope, tracked, isAuthoritative)
	if !ok {
		return
	}
	if client.Epoch() != sourceEpoch {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexDesktopStateApplyTimeout)
	defer cancel()
	err := client.broadcastForEpoch(ctx, sourceEpoch, "thread-stream-following-changed", map[string]any{
		"conversationId": status.ConversationID,
		"hostId":         status.HostID,
		"following":      true,
	}, []string{status.TargetClientID})
	if err != nil {
		log.Printf("[acp] Codex Desktop following 状态应答失败 thread=%q: %v", status.ConversationID, err)
	}
}

func codexDesktopFollowingStatusResponse(
	envelope codexDesktopEnvelope,
	tracked bool,
	authoritative bool,
) (codexDesktopFollowingStatus, bool) {
	if !tracked || !authoritative || strings.TrimSpace(envelope.SourceClientID) == "" {
		return codexDesktopFollowingStatus{}, false
	}
	var params struct {
		ConversationID string `json:"conversationId"`
		HostID         string `json:"hostId"`
	}
	if err := json.Unmarshal(envelope.Params, &params); err != nil {
		return codexDesktopFollowingStatus{}, false
	}
	status := codexDesktopFollowingStatus{
		ConversationID: strings.TrimSpace(params.ConversationID),
		HostID:         strings.TrimSpace(params.HostID),
		TargetClientID: strings.TrimSpace(envelope.SourceClientID),
	}
	if status.ConversationID == "" || status.HostID == "" {
		return codexDesktopFollowingStatus{}, false
	}
	return status, true
}

// codexDesktopBroadcastThreadID 只提取广播路由字段，不解析大型 conversationState。
func codexDesktopBroadcastThreadID(envelope codexDesktopEnvelope) string {
	var params struct {
		ConversationID string `json:"conversationId"`
	}
	_ = json.Unmarshal(envelope.Params, &params)
	return strings.TrimSpace(params.ConversationID)
}
