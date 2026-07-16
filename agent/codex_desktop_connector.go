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

const codexDesktopStateApplyTimeout = 10 * time.Second

type codexDesktopRuntime struct {
	mu       sync.Mutex
	client   *codexDesktopClient
	state    *codexDesktopStateStore
	actions  *codexDesktopActions
	owners   *codexRuntimeOwnerRegistry
	onEvents func(string, []*codexTurnEvent)
	tracked  map[string]bool
}

// newCodexDesktopRuntime 创建尚未连接 socket 的懒初始化 runtime。
func newCodexDesktopRuntime() *codexDesktopRuntime {
	return &codexDesktopRuntime{tracked: make(map[string]bool)}
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
		onBroadcast:  r.handleBroadcast,
		onDisconnect: r.handleDisconnect,
	})
	actions := newCodexDesktopActions(client, client.nextRequestID)
	state := newCodexDesktopStateStore(codexDesktopStateOptions{
		actions: actions,
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
	owners := r.owners
	r.mu.Unlock()
	if owners != nil {
		owners.markDesktopDisconnected()
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
	r.mu.Lock()
	if r.tracked == nil {
		r.tracked = make(map[string]bool)
	}
	r.tracked[strings.TrimSpace(threadID)] = true
	r.mu.Unlock()
}

// Presence 返回 socket 与 Codex 主进程存在性。
func (r *codexDesktopRuntime) Presence() (bool, bool) {
	return codexDesktopPresence()
}

// handleBroadcast 把状态广播投影到 owner registry 和统一 turn events。
func (r *codexDesktopRuntime) handleBroadcast(envelope codexDesktopEnvelope) {
	r.mu.Lock()
	client, state, owners, onEvents := r.client, r.state, r.owners, r.onEvents
	tracked := r.tracked[codexDesktopBroadcastThreadID(envelope)]
	r.mu.Unlock()
	if client == nil || state == nil {
		return
	}
	if envelope.Method == "thread-stream-state-changed" && !tracked {
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

// codexDesktopBroadcastThreadID 只提取广播路由字段，不解析大型 conversationState。
func codexDesktopBroadcastThreadID(envelope codexDesktopEnvelope) string {
	var params struct {
		ConversationID string `json:"conversationId"`
	}
	_ = json.Unmarshal(envelope.Params, &params)
	return strings.TrimSpace(params.ConversationID)
}
