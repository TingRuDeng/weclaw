package agent

import (
	"context"
	"fmt"
	"strings"
)

// RecoverCodexThread 在确认 Desktop 已释放后刷新 app-server 并恢复同一 thread。
func (a *ACPAgent) RecoverCodexThread(ctx context.Context, ref CodexThreadRef) error {
	ref.ConversationID = strings.TrimSpace(ref.ConversationID)
	ref.ThreadID = strings.TrimSpace(ref.ThreadID)
	binding, ok := a.CurrentCodexThreadBinding(ref.ConversationID)
	if !ok || binding.Ref.ThreadID != ref.ThreadID {
		return ErrCodexDesktopOwnershipUnknown
	}
	if err := validateCodexRecoveryBinding(binding); err != nil {
		return err
	}
	if a.hasActiveTurnChannel(ref.ThreadID) {
		return fmt.Errorf("Codex thread %s 仍有 active turn", ref.ThreadID)
	}
	if err := a.restartCodexAppServer(ctx); err != nil {
		return err
	}
	if err := a.resumeThread(ctx, ref.ConversationID, ref.ThreadID); err != nil {
		return fmt.Errorf("resume recovered thread %s: %w", ref.ThreadID, err)
	}
	a.mu.Lock()
	a.threads[ref.ConversationID] = ref.ThreadID
	delete(a.resumeOnFirstUse, ref.ConversationID)
	a.mu.Unlock()
	a.codexOwners.claimWeClawThread(ref.ThreadID, binding.State)
	a.persistState()
	return nil
}

func validateCodexRecoveryBinding(binding CodexThreadBinding) error {
	switch binding.Owner {
	case CodexOwnerDesktopLive, CodexOwnerUnknown:
		return ErrCodexDesktopOwnershipUnknown
	case CodexOwnerDesktopDisconnected:
		return ErrCodexDesktopDisconnected
	case CodexOwnerPersistedOnly:
		if binding.ReleaseConfirmed {
			return nil
		}
		return ErrCodexDesktopOwnershipUnknown
	default:
		return fmt.Errorf("Codex thread owner %q 不允许恢复", binding.Owner)
	}
}

// restartCodexAppServer 仅刷新 ACP subprocess，不关闭独立 Desktop connector。
func (a *ACPAgent) restartCodexAppServer(ctx context.Context) error {
	if a.restartCodexAppServerCall != nil {
		return a.restartCodexAppServerCall(ctx)
	}
	a.stopCodexAppServerProcess()
	if err := a.ensureStarted(ctx); err != nil {
		return fmt.Errorf("restart Codex app-server: %w", err)
	}
	a.mu.Lock()
	for conversationID := range a.threads {
		a.resumeOnFirstUse[conversationID] = true
	}
	a.mu.Unlock()
	return nil
}

// stopCodexAppServerProcess 停止 ACP 子进程和 app-server waiters，不触碰 Desktop runtime。
func (a *ACPAgent) stopCodexAppServerProcess() {
	a.mu.Lock()
	stdin, cmd := a.stdin, a.cmd
	a.started, a.stdin, a.cmd, a.scanner = false, nil, nil, nil
	a.mu.Unlock()
	stopACPProcess(stdin, cmd)
	a.failPendingRequests("ACP runtime stopped for recovery")
	a.failAppServerActiveTurns("ACP runtime stopped for recovery")
}

// hasActiveTurnChannel 防止恢复覆盖同一 thread 的进行中任务。
func (a *ACPAgent) hasActiveTurnChannel(threadID string) bool {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	_, active := a.turnCh[threadID]
	return active
}
