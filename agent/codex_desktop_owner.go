package agent

import (
	"strings"
	"sync"
)

type codexRuntimeOwnerRegistry struct {
	mu            sync.Mutex
	threads       map[string]CodexThreadBinding
	conversations map[string]string
	leases        map[string]*codexWriterLeaseState
	// enforceControl only exists for the retired Desktop bridge compatibility
	// path. The shared app-server has one writer authority and does not assign
	// exclusive ownership to individual frontend routes.
	enforceControl bool
}

// newCodexRuntimeOwnerRegistry 创建独立于 ACP threads map 的 owner registry。
func newCodexRuntimeOwnerRegistry(probe codexDesktopOwnerProbe) *codexRuntimeOwnerRegistry {
	return &codexRuntimeOwnerRegistry{
		threads:       make(map[string]CodexThreadBinding),
		conversations: make(map[string]string), leases: make(map[string]*codexWriterLeaseState),
		enforceControl: probe != nil,
	}
}

// observeDesktopSnapshot 按 writer lease 核对 Desktop 快照，不能证明同一 turn 时进入冲突态。
func (r *codexRuntimeOwnerRegistry) observeDesktopSnapshot(threadID string, revision uint64, state CodexThreadState) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observeDesktopSnapshotLocked(strings.TrimSpace(threadID), revision, state)
}

// claimWeClawThread 标记本地 app-server 已实际持有 thread。
func (r *codexRuntimeOwnerRegistry) claimWeClawThread(threadID string, state CodexThreadState) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.threads[threadID]
	generation := nextCodexRuntimeGeneration(current, CodexRuntimeWeClaw)
	binding := CodexThreadBinding{
		Ref: CodexThreadRef{ThreadID: threadID}, State: state,
		Control: current.Control, Runtime: CodexRuntimeWeClaw, RuntimeGeneration: generation,
	}
	r.threads[threadID] = binding
	return binding
}

// claimWeClawConversation 原子声明 app-server owner 并把 conversation 切换到同一 thread。
func (r *codexRuntimeOwnerRegistry) claimWeClawConversation(ref CodexThreadRef, state CodexThreadState) CodexThreadBinding {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	current := r.threads[ref.ThreadID]
	state.ThreadID = ref.ThreadID
	generation := nextCodexRuntimeGeneration(current, CodexRuntimeWeClaw)
	binding := CodexThreadBinding{
		Ref: ref, State: state,
		Control: current.Control, Runtime: CodexRuntimeWeClaw, RuntimeGeneration: generation,
	}
	r.threads[ref.ThreadID] = binding
	r.conversations[ref.ConversationID] = ref.ThreadID
	return binding
}

// unbindConversation 删除 conversation 路由，不改变 thread 本身的 owner 证据。
func (r *codexRuntimeOwnerRegistry) unbindConversation(conversationID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conversations, strings.TrimSpace(conversationID))
}

// markDesktopDisconnected 降级 Desktop runtime，但不改变控制意图，也不产生 release evidence。
func (r *codexRuntimeOwnerRegistry) markDesktopDisconnected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for threadID, binding := range r.threads {
		if codexBindingRuntime(binding) != CodexRuntimeDesktop {
			continue
		}
		binding.Runtime = CodexRuntimeUnknown
		binding.RuntimeGeneration++
		r.threads[threadID] = binding
	}
}

// threadBinding 返回指定 thread 的当前权威 owner 快照。
func (r *codexRuntimeOwnerRegistry) threadBinding(threadID string) (CodexThreadBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.threads[strings.TrimSpace(threadID)]
	binding.Runtime = codexBindingRuntime(binding)
	return binding, ok
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
	binding.Runtime = codexBindingRuntime(binding)
	return binding, true
}

// restoreBindings 只迁移旧 conversation/thread 关联，实际 runtime 重启后统一未知。
func (r *codexRuntimeOwnerRegistry) restoreBindings(bindings map[string]CodexThreadBinding) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	loaded := 0
	for conversationID, binding := range bindings {
		threadID := strings.TrimSpace(binding.Ref.ThreadID)
		if conversationID == "" || threadID == "" {
			continue
		}
		binding = CodexThreadBinding{
			Ref:               CodexThreadRef{ConversationID: conversationID, ThreadID: threadID},
			Runtime:           CodexRuntimeUnknown,
			RuntimeGeneration: 1, State: binding.State,
		}
		r.threads[threadID] = binding
		r.conversations[conversationID] = threadID
		loaded++
	}
	return loaded
}
