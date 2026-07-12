package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type codexRuntimeOwnerRegistry struct {
	mu            sync.Mutex
	probe         codexDesktopOwnerProbe
	threads       map[string]CodexThreadBinding
	conversations map[string]string
}

// newCodexRuntimeOwnerRegistry 创建独立于 ACP threads map 的 owner registry。
func newCodexRuntimeOwnerRegistry(probe codexDesktopOwnerProbe) *codexRuntimeOwnerRegistry {
	return &codexRuntimeOwnerRegistry{
		probe: probe, threads: make(map[string]CodexThreadBinding),
		conversations: make(map[string]string),
	}
}

// observeDesktopSnapshot 仅声明未占用 thread，不抢占 WeClaw runtime。
func (r *codexRuntimeOwnerRegistry) observeDesktopSnapshot(threadID string, revision uint64, state CodexThreadState) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	threadID = strings.TrimSpace(threadID)
	current := r.threads[threadID]
	if current.Owner == CodexOwnerWeClawRuntime {
		return current
	}
	if current.OwnerRevision > revision && current.Owner == CodexOwnerDesktopLive {
		return current
	}
	binding := CodexThreadBinding{
		Ref: CodexThreadRef{ThreadID: threadID}, Owner: CodexOwnerDesktopLive,
		OwnerRevision: revision, Connected: true, State: state,
	}
	r.threads[threadID] = binding
	return binding
}

// claimWeClawThread 标记本地 app-server 已实际持有 thread。
func (r *codexRuntimeOwnerRegistry) claimWeClawThread(threadID string, state CodexThreadState) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.threads[threadID]
	binding := CodexThreadBinding{
		Ref: CodexThreadRef{ThreadID: threadID}, Owner: CodexOwnerWeClawRuntime,
		OwnerRevision: current.OwnerRevision + 1, Connected: true, State: state,
	}
	r.threads[threadID] = binding
	return binding
}

// markDesktopDisconnected 降级 Desktop owner，但不产生 release evidence。
func (r *codexRuntimeOwnerRegistry) markDesktopDisconnected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for threadID, binding := range r.threads {
		if binding.Owner != CodexOwnerDesktopLive {
			continue
		}
		binding.Owner = CodexOwnerDesktopDisconnected
		binding.Connected = false
		binding.OwnerRevision++
		r.threads[threadID] = binding
	}
}

// confirmDesktopReleased 仅把 Desktop 明确拒绝处理的 live thread 标记为可恢复。
func (r *codexRuntimeOwnerRegistry) confirmDesktopReleased(threadID string) (CodexThreadBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	threadID = strings.TrimSpace(threadID)
	binding, ok := r.threads[threadID]
	if !ok || binding.Owner != CodexOwnerDesktopLive {
		return binding, false
	}
	binding.Owner = CodexOwnerPersistedOnly
	binding.OwnerRevision++
	binding.Connected = false
	binding.ReleaseConfirmed = true
	r.threads[threadID] = binding
	return binding, true
}

// threadBinding 返回指定 thread 的当前权威 owner 快照。
func (r *codexRuntimeOwnerRegistry) threadBinding(threadID string) (CodexThreadBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.threads[strings.TrimSpace(threadID)]
	return binding, ok
}

// bind 按 snapshot、discovery、history 和进程 presence 顺序只读探测 owner。
func (r *codexRuntimeOwnerRegistry) bind(ctx context.Context, ref CodexThreadRef) (CodexThreadBinding, error) {
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	if binding, ok := r.threadBinding(ref.ThreadID); ok {
		switch binding.Owner {
		case CodexOwnerDesktopLive, CodexOwnerWeClawRuntime, CodexOwnerPersistedOnly:
			return r.bindConversation(ref, binding), nil
		}
	}
	if r.probe == nil {
		return r.recordProbeResult(ref, CodexOwnerUnknown, false), ErrCodexDesktopOwnershipUnknown
	}
	_, _ = r.probe.Discover(ctx, ref)
	loadErr := r.probe.LoadHistory(ctx, ref)
	if binding, ok := r.threadBinding(ref.ThreadID); ok && binding.Owner == CodexOwnerDesktopLive {
		return r.bindConversation(ref, binding), nil
	}
	return r.classifyProbeResult(ref, loadErr)
}

// classifyProbeResult 只接受明确无人处理或进程消失作为 release evidence。
func (r *codexRuntimeOwnerRegistry) classifyProbeResult(ref CodexThreadRef, loadErr error) (CodexThreadBinding, error) {
	if errors.Is(loadErr, ErrCodexDesktopNoClient) {
		return r.recordProbeResult(ref, CodexOwnerPersistedOnly, true), nil
	}
	socketExists, processExists := r.probe.Presence()
	if !socketExists && !processExists {
		return r.recordProbeResult(ref, CodexOwnerPersistedOnly, true), nil
	}
	if binding, ok := r.threadBinding(ref.ThreadID); ok && binding.Owner == CodexOwnerDesktopDisconnected {
		return r.bindConversation(ref, binding), ErrCodexDesktopDisconnected
	}
	return r.recordProbeResult(ref, CodexOwnerUnknown, false), ErrCodexDesktopOwnershipUnknown
}

// recordProbeResult 原子更新 thread owner 和 conversation 选择。
func (r *codexRuntimeOwnerRegistry) recordProbeResult(ref CodexThreadRef, owner CodexRuntimeOwner, released bool) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.threads[ref.ThreadID]
	binding := CodexThreadBinding{
		Ref: ref, Owner: owner, OwnerRevision: current.OwnerRevision + 1,
		ReleaseConfirmed: released, State: current.State,
	}
	r.threads[ref.ThreadID] = binding
	r.conversations[ref.ConversationID] = ref.ThreadID
	return binding
}

// bindConversation 只记录路由选择，不改变 thread 的权威 owner。
func (r *codexRuntimeOwnerRegistry) bindConversation(ref CodexThreadRef, binding CodexThreadBinding) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conversations[ref.ConversationID] = ref.ThreadID
	binding.Ref = ref
	return binding
}

// currentConversationBinding 返回 conversation 当前选择的 thread binding。
func (r *codexRuntimeOwnerRegistry) currentConversationBinding(conversationID string) (CodexThreadBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	threadID := r.conversations[strings.TrimSpace(conversationID)]
	binding, ok := r.threads[threadID]
	if !ok {
		return CodexThreadBinding{}, false
	}
	binding.Ref.ConversationID = strings.TrimSpace(conversationID)
	return binding, true
}

// persistedBindings 把进程内 owner 转换为重启后可安全恢复的 conversation 快照。
func (r *codexRuntimeOwnerRegistry) persistedBindings() map[string]CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]CodexThreadBinding, len(r.conversations))
	for conversationID, threadID := range r.conversations {
		binding, ok := r.threads[threadID]
		if !ok {
			continue
		}
		binding = restartSafeCodexBinding(binding)
		binding.Ref = CodexThreadRef{ConversationID: conversationID, ThreadID: threadID}
		result[conversationID] = binding
	}
	return result
}

// restoreBindings 恢复 owner 快照；旧版本写入的进程内 owner 也按重启语义迁移。
func (r *codexRuntimeOwnerRegistry) restoreBindings(bindings map[string]CodexThreadBinding) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	loaded := 0
	for conversationID, binding := range bindings {
		threadID := strings.TrimSpace(binding.Ref.ThreadID)
		if conversationID == "" || threadID == "" {
			continue
		}
		binding = restartSafeCodexBinding(binding)
		binding.Ref = CodexThreadRef{ConversationID: conversationID, ThreadID: threadID}
		r.threads[threadID] = binding
		r.conversations[conversationID] = threadID
		loaded++
	}
	return loaded
}

// restartSafeCodexBinding 保留确定性释放证据，但不把 Desktop 断线误判为释放。
func restartSafeCodexBinding(binding CodexThreadBinding) CodexThreadBinding {
	binding.Connected = false
	switch binding.Owner {
	case CodexOwnerDesktopLive:
		binding.Owner = CodexOwnerDesktopDisconnected
	case CodexOwnerWeClawRuntime:
		binding.Owner = CodexOwnerPersistedOnly
		binding.ReleaseConfirmed = true
	}
	return binding
}
