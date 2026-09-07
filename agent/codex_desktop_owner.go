package agent

import (
	"strings"
	"sync"
)

type codexRuntimeOwnerRegistry struct {
	mu                          sync.Mutex
	threads                     map[string]CodexThreadBinding
	conversations               map[string]string
	conversationRevisions       map[string]uint64
	conversationRevisionCounter uint64
	leases                      map[string]*codexWriterLeaseState
	archivedThreads             map[string]bool
	// enforceControl only exists for the retired Desktop bridge compatibility
	// path. The shared app-server has one writer authority and does not assign
	// exclusive ownership to individual frontend routes.
	enforceControl bool
}

// newCodexRuntimeOwnerRegistry 创建独立于 ACP threads map 的 owner registry。
func newCodexRuntimeOwnerRegistry(probe codexDesktopOwnerProbe) *codexRuntimeOwnerRegistry {
	return &codexRuntimeOwnerRegistry{
		threads:               make(map[string]CodexThreadBinding),
		conversations:         make(map[string]string),
		conversationRevisions: make(map[string]uint64),
		leases:                make(map[string]*codexWriterLeaseState),
		archivedThreads:       make(map[string]bool),
		enforceControl:        probe != nil,
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
	r.advanceConversationRevisionLocked(ref.ConversationID)
	return r.claimWeClawConversationLocked(ref, state)
}

func (r *codexRuntimeOwnerRegistry) claimWeClawConversationAtRevision(
	ref CodexThreadRef,
	state CodexThreadState,
	revision uint64,
) (CodexThreadBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	if r.conversationRevisions[ref.ConversationID] != revision {
		return CodexThreadBinding{}, ErrCodexControlChanged
	}
	if r.archivedThreads[ref.ThreadID] {
		return CodexThreadBinding{}, ErrCodexControlChanged
	}
	return r.claimWeClawConversationLocked(ref, state), nil
}

func (r *codexRuntimeOwnerRegistry) unarchiveThread(threadID string) {
	r.mu.Lock()
	delete(r.archivedThreads, strings.TrimSpace(threadID))
	r.mu.Unlock()
}

func (r *codexRuntimeOwnerRegistry) claimWeClawConversationLocked(ref CodexThreadRef, state CodexThreadState) CodexThreadBinding {
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

// invalidateConversationBinding advances the route revision and clears the
// current selection in one registry critical section. The returned revision
// can be reused by Reset to conditionally bind the replacement thread.
func (r *codexRuntimeOwnerRegistry) invalidateConversationBinding(conversationID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	conversationID = strings.TrimSpace(conversationID)
	revision := r.advanceConversationRevisionLocked(conversationID)
	delete(r.conversations, conversationID)
	return revision
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

// switchRuntimeAuthority 废止不属于新 Host 的缓存 runtime；冲突态不会被静默清除。
func (r *codexRuntimeOwnerRegistry) switchRuntimeAuthority(runtime CodexRuntimeHolder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for threadID, binding := range r.threads {
		current := codexBindingRuntime(binding)
		if current == CodexRuntimeConflict || current == CodexRuntimeUnknown || current == runtime {
			continue
		}
		binding.Runtime = CodexRuntimeUnknown
		binding.RuntimeGeneration++
		r.threads[threadID] = binding
	}
}

// invalidateRuntimeAuthority discards snapshots owned by a Host process that
// has been restarted without changing the logical runtime kind. Durable
// conversation mappings and control intents remain available for resume.
func (r *codexRuntimeOwnerRegistry) invalidateRuntimeAuthority(runtime CodexRuntimeHolder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for threadID, binding := range r.threads {
		if codexBindingRuntime(binding) != runtime {
			continue
		}
		binding.Runtime = CodexRuntimeUnknown
		binding.RuntimeGeneration++
		r.threads[threadID] = binding
	}
}

func (r *codexRuntimeOwnerRegistry) enforcesControl() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enforceControl
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
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	r.advanceConversationRevisionLocked(ref.ConversationID)
	r.conversations[ref.ConversationID] = ref.ThreadID
	binding.Ref = ref
	return binding
}

func (r *codexRuntimeOwnerRegistry) bindConversationAtRevision(
	ref CodexThreadRef,
	binding CodexThreadBinding,
	revision uint64,
) (CodexThreadBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	if r.conversationRevisions[ref.ConversationID] != revision {
		return binding, ErrCodexControlChanged
	}
	r.conversations[ref.ConversationID] = ref.ThreadID
	binding.Ref = ref
	return binding, nil
}

func (r *codexRuntimeOwnerRegistry) beginConversationBindingIntent(conversationID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.advanceConversationRevisionLocked(strings.TrimSpace(conversationID))
}

func (r *codexRuntimeOwnerRegistry) currentConversationBindingRevision(ref CodexThreadRef) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conversationID := strings.TrimSpace(ref.ConversationID)
	threadID := strings.TrimSpace(ref.ThreadID)
	if conversationID == "" || threadID == "" || strings.TrimSpace(r.conversations[conversationID]) != threadID {
		return 0, false
	}
	return r.ensureConversationRevisionLocked(conversationID), true
}

func (r *codexRuntimeOwnerRegistry) conversationBindingRevisionMatches(
	ref CodexThreadRef,
	revision uint64,
	expectedThreadID string,
) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conversationBindingRevisionMatchesLocked(
		ref.ConversationID, revision, expectedThreadID,
	)
}

func (r *codexRuntimeOwnerRegistry) conversationBindingRevisionMatchesLocked(
	conversationID string,
	revision uint64,
	expectedThreadID string,
) bool {
	conversationID = strings.TrimSpace(conversationID)
	if revision == 0 || r.conversationRevisions[conversationID] != revision {
		return false
	}
	expectedThreadID = strings.TrimSpace(expectedThreadID)
	return expectedThreadID == "" || strings.TrimSpace(r.conversations[conversationID]) == expectedThreadID
}

func (r *codexRuntimeOwnerRegistry) advanceConversationRevisionLocked(conversationID string) uint64 {
	if conversationID == "" {
		return 0
	}
	r.conversationRevisionCounter++
	r.conversationRevisions[conversationID] = r.conversationRevisionCounter
	return r.conversationRevisionCounter
}

func (r *codexRuntimeOwnerRegistry) ensureConversationRevisionLocked(conversationID string) uint64 {
	if revision := r.conversationRevisions[conversationID]; revision != 0 {
		return revision
	}
	return r.advanceConversationRevisionLocked(conversationID)
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
		r.ensureConversationRevisionLocked(conversationID)
		loaded++
	}
	return loaded
}
