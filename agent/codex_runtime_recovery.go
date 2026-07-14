package agent

import (
	"context"
	"fmt"
)

// restartCodexAppServer 仅刷新 ACP subprocess，不关闭独立 Desktop connector。
func (a *ACPAgent) restartCodexAppServer(ctx context.Context) error {
	return a.ensureCodexAppServerGate().drain(ctx, a.restartCodexAppServerUnsafe)
}

func (a *ACPAgent) restartCodexAppServerUnsafe(ctx context.Context) error {
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

func (a *ACPAgent) ensureCodexAppServerGate() *codexAppServerGate {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.appServerGate == nil {
		a.appServerGate = newCodexAppServerGate()
	}
	return a.appServerGate
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
