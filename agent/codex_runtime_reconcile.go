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
	if !ok || !sameCodexControlIntent(binding.Control, req.Intent) {
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
