package agent

import (
	"fmt"
	"log"
	"strings"
)

type codexDesktopLeaseObservation struct {
	threadID string
	state    CodexThreadState
	current  CodexThreadBinding
	lease    *codexWriterLeaseState
}

func (r *codexRuntimeOwnerRegistry) observeDesktopSnapshotLocked(threadID string, _ uint64, state CodexThreadState) CodexThreadBinding {
	current := r.threads[threadID]
	state.ThreadID = threadID
	if lease := r.leases[threadID]; lease != nil {
		return r.observeDesktopLeaseLocked(codexDesktopLeaseObservation{
			threadID: threadID, state: state, current: current, lease: lease,
		})
	}
	if current.Runtime == CodexRuntimeConflict {
		current.State = state
		r.threads[threadID] = current
		return current
	}
	if current.Control.Owner == CodexControlRemote && !sameObservedDesktopTurn(current.State, state) {
		current.State = state
		r.threads[threadID] = current
		_ = r.markConflictLocked(threadID, "Desktop 在远程控制期间开始了未授权任务")
		return r.threads[threadID]
	}
	current.Ref.ThreadID = threadID
	current.RuntimeGeneration = nextCodexRuntimeGeneration(current, CodexRuntimeDesktop)
	current.Runtime = CodexRuntimeDesktop
	current.State = state
	current.ConflictReason = ""
	r.threads[threadID] = current
	return current
}

// sameObservedDesktopTurn 允许移交时已经存在的 Desktop turn 继续被远程观察。
func sameObservedDesktopTurn(current CodexThreadState, observed CodexThreadState) bool {
	activeTurnID := strings.TrimSpace(current.ActiveTurnID)
	if observed.Active {
		return current.Active && activeTurnID != "" && activeTurnID == strings.TrimSpace(observed.ActiveTurnID)
	}
	lastTurnID := strings.TrimSpace(observed.LastTurnID)
	return lastTurnID == "" || lastTurnID == activeTurnID ||
		lastTurnID == strings.TrimSpace(current.LastTurnID)
}

func (r *codexRuntimeOwnerRegistry) observeDesktopLeaseLocked(observation codexDesktopLeaseObservation) CodexThreadBinding {
	threadID := observation.threadID
	state := observation.state
	current := observation.current
	lease := observation.lease
	activeTurnID := strings.TrimSpace(state.ActiveTurnID)
	lastTurnID := strings.TrimSpace(state.LastTurnID)
	if state.Active && lease.turnID == "" {
		lease.candidateDesktopTurn = activeTurnID
	} else if state.Active && (activeTurnID == "" || activeTurnID != lease.turnID) {
		r.threads[threadID] = current
		_ = r.markConflictLocked(threadID, "Desktop active turn 与远程 writer lease 不一致")
		return r.threads[threadID]
	}
	if !state.Active && lastTurnID != "" && lastTurnID != lease.baselineLastTurnID {
		if lease.turnID == "" {
			lease.candidateDesktopTurn = lastTurnID
		} else if lastTurnID != lease.turnID {
			r.threads[threadID] = current
			_ = r.markConflictLocked(threadID, "Desktop completed turn 与远程 writer lease 不一致")
			return r.threads[threadID]
		}
	}
	current.State = state
	r.threads[threadID] = current
	return current
}

func (r *codexRuntimeOwnerRegistry) markConflictLocked(threadID string, reason string) error {
	binding := r.threads[threadID]
	if binding.Runtime != CodexRuntimeConflict {
		log.Printf("[codex-runtime] 检测到写入冲突 thread=%q owner=%q revision=%d active=%t activeTurn=%q lastTurn=%q reason=%q",
			threadID, binding.Control.Owner, binding.Control.Revision, binding.State.Active,
			binding.State.ActiveTurnID, binding.State.LastTurnID, strings.TrimSpace(reason))
	}
	binding.RuntimeGeneration = nextCodexRuntimeGeneration(binding, CodexRuntimeConflict)
	binding.Runtime = CodexRuntimeConflict
	binding.ConflictReason = strings.TrimSpace(reason)
	r.threads[threadID] = binding
	if lease := r.leases[threadID]; lease != nil {
		lease.conflict = true
		lease.conflictOnce.Do(func() { close(lease.conflictCh) })
	}
	return fmt.Errorf("%w: %s", ErrCodexRuntimeConflict, binding.ConflictReason)
}

// markRuntimeConflict 只允许同 revision 或更高 revision 的持久化控制意图登记 conflict。
func (r *codexRuntimeOwnerRegistry) markRuntimeConflict(req CodexRuntimeRequest, reason string) (CodexThreadBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	threadID := strings.TrimSpace(req.Ref.ThreadID)
	binding := r.threads[threadID]
	if codexControlIntentEstablished(binding.Control) {
		if binding.Control.Revision > req.Intent.Revision ||
			(binding.Control.Revision == req.Intent.Revision && !sameCodexControlIntent(binding.Control, req.Intent)) {
			return binding, ErrCodexControlChanged
		}
	}
	binding.Ref = req.Ref
	binding.Control = req.Intent
	if binding.State.ThreadID == "" {
		binding.State.ThreadID = threadID
	}
	r.threads[threadID] = binding
	if conversationID := strings.TrimSpace(req.Ref.ConversationID); conversationID != "" {
		r.conversations[conversationID] = threadID
	}
	_ = r.markConflictLocked(threadID, reason)
	return r.threads[threadID], nil
}

func codexControlIntentEstablished(intent CodexControlIntent) bool {
	return intent.Owner != "" || intent.RouteKey != "" || intent.ConversationID != "" || intent.Revision != 0
}

func nextCodexRuntimeGeneration(binding CodexThreadBinding, runtime CodexRuntimeHolder) uint64 {
	generation := binding.RuntimeGeneration
	if generation == 0 || codexBindingRuntime(binding) != runtime {
		generation++
	}
	return generation
}

func codexBindingRuntime(binding CodexThreadBinding) CodexRuntimeHolder {
	return binding.Runtime
}
