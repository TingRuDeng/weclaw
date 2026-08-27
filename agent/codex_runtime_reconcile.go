package agent

import (
	"fmt"
	"strings"
)

// reconcileObservedTurn 把已由远程窗口观察的同一 Desktop turn 同步到 owner registry。
// 它只收敛同一控制 revision 下的已知 turn，不会解除冲突态或覆盖正在写入的 lease。
func (r *codexRuntimeOwnerRegistry) reconcileObservedTurn(req CodexRuntimeRequest, state CodexThreadState) (CodexThreadBinding, error) {
	if err := validateRemoteCodexRequest(req); err != nil {
		return CodexThreadBinding{}, err
	}
	threadID := strings.TrimSpace(req.Ref.ThreadID)
	stateThreadID := strings.TrimSpace(state.ThreadID)
	if stateThreadID != "" && stateThreadID != threadID {
		return CodexThreadBinding{}, fmt.Errorf("Codex observed turn thread 不一致")
	}
	state.ThreadID = threadID
	turnID, err := observedCodexTurnID(state)
	if err != nil {
		return CodexThreadBinding{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.threads[threadID]
	if !ok || r.enforceControl && !sameCodexControlIntent(binding.Control, req.Intent) {
		return binding, ErrCodexControlChanged
	}
	if r.leases[threadID] != nil {
		return binding, ErrCodexWriterBusy
	}
	if binding.Runtime == CodexRuntimeConflict {
		return binding, ErrCodexRuntimeConflict
	}
	if state.Active {
		if binding.Runtime != CodexRuntimeDesktop {
			return binding, ErrCodexRuntimeUnavailable
		}
		if activeTurnID := strings.TrimSpace(binding.State.ActiveTurnID); binding.State.Active &&
			(activeTurnID == "" || activeTurnID != turnID) {
			err := r.markConflictLocked(threadID, "观察到的 Desktop active turn 与当前远程任务不一致")
			return r.threads[threadID], err
		}
	} else {
		if binding.Runtime != CodexRuntimeDesktop && binding.Runtime != CodexRuntimeUnknown {
			return binding, ErrCodexRuntimeUnavailable
		}
		activeTurnID := strings.TrimSpace(binding.State.ActiveTurnID)
		if binding.State.Active && (activeTurnID == "" || activeTurnID != turnID) {
			err := r.markConflictLocked(threadID, "观察到的 Desktop terminal turn 与当前远程任务不一致")
			return r.threads[threadID], err
		}
		lastTurnID := strings.TrimSpace(binding.State.LastTurnID)
		if !binding.State.Active && lastTurnID != turnID {
			return binding, ErrCodexControlChanged
		}
	}
	binding.State = state
	r.threads[threadID] = binding
	return binding, nil
}

// reconcileSharedHostObservedTurn 收敛同一官方 daemon 上的只读前端观察。
//
// shared Host 只有一个写入 authority；因此本地 WeClaw turn 持有 writer
// lease 时，另一个前端可以观察同一个 turn，但不能借观察快照替换 lease、
// 改写 runtime generation，或把不同 turn 当成同一任务。这个路径只在
// ReconcileCodexObservedTurn 已确认官方 daemon 为 Host 时调用。
func (r *codexRuntimeOwnerRegistry) reconcileSharedHostObservedTurn(req CodexRuntimeRequest, state CodexThreadState) (CodexThreadBinding, error) {
	if err := validateCodexRuntimeRequestForRegistry(req, r.enforceControl); err != nil {
		return CodexThreadBinding{}, err
	}
	threadID := strings.TrimSpace(req.Ref.ThreadID)
	if stateThreadID := strings.TrimSpace(state.ThreadID); stateThreadID != "" && stateThreadID != threadID {
		return CodexThreadBinding{}, fmt.Errorf("Codex observed turn thread 不一致")
	}
	state.ThreadID = threadID
	turnID, err := observedCodexTurnID(state)
	if err != nil {
		return CodexThreadBinding{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.threads[threadID]
	if !ok {
		return binding, ErrCodexControlChanged
	}
	if r.enforceControl && !sameCodexControlIntent(binding.Control, req.Intent) {
		return binding, ErrCodexControlChanged
	}
	if binding.Runtime == CodexRuntimeConflict {
		return binding, ErrCodexRuntimeConflict
	}
	if binding.Runtime != CodexRuntimeWeClaw {
		return binding, ErrCodexRuntimeUnavailable
	}

	if lease := r.leases[threadID]; lease != nil {
		expectedTurnID := strings.TrimSpace(lease.turnID)
		if expectedTurnID == "" || expectedTurnID != turnID {
			return binding, ErrCodexWriterBusy
		}
	}
	if state.Active && binding.State.Active {
		activeTurnID := strings.TrimSpace(binding.State.ActiveTurnID)
		if activeTurnID == "" || activeTurnID != turnID {
			err := r.markConflictLocked(threadID, "共享 Host 观察到的 active turn 与当前任务不一致")
			return r.threads[threadID], err
		}
	}
	if !state.Active && binding.State.Active {
		activeTurnID := strings.TrimSpace(binding.State.ActiveTurnID)
		if activeTurnID == "" || activeTurnID != turnID {
			err := r.markConflictLocked(threadID, "共享 Host 观察到的 terminal turn 与当前任务不一致")
			return r.threads[threadID], err
		}
	}
	if !state.Active && !binding.State.Active {
		lastTurnID := strings.TrimSpace(binding.State.LastTurnID)
		if lastTurnID != "" && lastTurnID != turnID {
			return binding, ErrCodexControlChanged
		}
	}
	binding.Ref = req.Ref
	binding.State = state
	r.threads[threadID] = binding
	r.conversations[req.Ref.ConversationID] = threadID
	return binding, nil
}

func observedCodexTurnID(state CodexThreadState) (string, error) {
	turnID := strings.TrimSpace(state.LastTurnID)
	if state.Active {
		turnID = strings.TrimSpace(state.ActiveTurnID)
	}
	if turnID == "" {
		return "", fmt.Errorf("Codex observed turn 缺少 turn ID")
	}
	return turnID, nil
}
