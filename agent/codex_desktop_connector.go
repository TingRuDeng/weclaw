package agent

import (
	"context"
	"log"
	"sync"
)

type codexDesktopRuntime struct {
	mu       sync.Mutex
	client   *codexDesktopClient
	state    *codexDesktopStateStore
	actions  *codexDesktopActions
	owners   *codexRuntimeOwnerRegistry
	onEvents func(string, []*codexTurnEvent)
}

// newCodexDesktopRuntime 创建尚未连接 socket 的懒初始化 runtime。
func newCodexDesktopRuntime() *codexDesktopRuntime {
	return &codexDesktopRuntime{}
}

// setOwnerRegistry 建立 snapshot 到 owner registry 的单向通知。
func (r *codexDesktopRuntime) setOwnerRegistry(owners *codexRuntimeOwnerRegistry) {
	r.mu.Lock()
	r.owners = owners
	r.mu.Unlock()
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

// ensureInitialized 首次使用时才创建 IPC client、actions 和 state store。
func (r *codexDesktopRuntime) ensureInitialized() *codexDesktopClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client
	}
	client := newCodexDesktopClient(codexDesktopClientOptions{
		onBroadcast: r.handleBroadcast,
	})
	actions := newCodexDesktopActions(client, client.nextRequestID)
	state := newCodexDesktopStateStore(codexDesktopStateOptions{
		actions: actions,
		requestSnapshot: func(threadID string) {
			ref := CodexThreadRef{ThreadID: threadID}
			if err := r.LoadHistory(context.Background(), ref); err != nil {
				log.Printf("[acp] Desktop snapshot recovery failed (thread=%s): %v", threadID, err)
			}
		},
	})
	r.client, r.actions, r.state = client, actions, state
	return client
}

// Discover 探测是否有 Desktop client 能处理目标 thread 的历史请求。
func (r *codexDesktopRuntime) Discover(ctx context.Context, ref CodexThreadRef) (bool, error) {
	client := r.ensureInitialized()
	if err := client.Connect(ctx); err != nil {
		return false, err
	}
	return client.Discover(ctx, codexDesktopRequestSpec{
		Method: "thread-follower-load-complete-history",
		Params: map[string]string{"conversationId": ref.ThreadID},
	})
}

// LoadHistory 请求 Desktop 广播目标 thread 的完整 conversation state。
func (r *codexDesktopRuntime) LoadHistory(ctx context.Context, ref CodexThreadRef) error {
	client := r.ensureInitialized()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	_, err := client.Call(ctx, "thread-follower-load-complete-history", map[string]string{
		"conversationId": ref.ThreadID,
	})
	return err
}

// Presence 返回 socket 与 Codex 主进程存在性。
func (r *codexDesktopRuntime) Presence() (bool, bool) {
	return codexDesktopPresence()
}

// handleBroadcast 把状态广播投影到 owner registry 和统一 turn events。
func (r *codexDesktopRuntime) handleBroadcast(envelope codexDesktopEnvelope) {
	r.mu.Lock()
	client, state, owners, onEvents := r.client, r.state, r.owners, r.onEvents
	r.mu.Unlock()
	if client == nil || state == nil {
		return
	}
	update, err := state.applyEnvelope(client.Epoch(), envelope)
	if err != nil {
		log.Printf("[acp] Desktop state projection failed: %v", err)
		return
	}
	if owners != nil && update.Applied {
		owners.observeDesktopSnapshot(update.Snapshot.ThreadID, update.Snapshot.Revision, update.Snapshot.State)
	}
	if onEvents != nil && len(update.Events) > 0 {
		onEvents(update.Snapshot.ThreadID, update.Events)
	}
}
