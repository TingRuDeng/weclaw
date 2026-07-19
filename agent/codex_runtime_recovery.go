package agent

import (
	"context"
	"fmt"
	"strings"
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

// ensureCodexAppServerStartedForTurn 修复 app-server 在仅 resume 后退出的场景：
// 下一条普通消息必须先重启 runtime，并重新 resume 已绑定 thread，而不是直接写已关闭的 stdin。
func (a *ACPAgent) ensureCodexAppServerStartedForTurn(ctx context.Context, conversationID string) error {
	if a.protocol != protocolCodexAppServer || a.rpcCall != nil || a.isRuntimeStarted() {
		return nil
	}
	if err := a.ensureStarted(ctx); err != nil {
		return fmt.Errorf("start Codex app-server runtime: %w", err)
	}
	a.mu.Lock()
	if strings.TrimSpace(a.threads[conversationID]) != "" {
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

// stopCodexAppServerProcess 只断开当前 app-server 客户端连接。共享 host
// 是独立运行边界，普通恢复不能终止其他前端仍在使用的唯一服务进程。
func (a *ACPAgent) stopCodexAppServerProcess() {
	if a.usesCodexSharedHost() {
		connection, _, _ := a.disconnectCodexHostClient(false)
		if connection != nil {
			_ = connection.Close()
		}
		a.failAppServerActiveTurns("Codex app-server client disconnected for recovery")
		a.failPendingRequests("Codex app-server client disconnected for recovery")
		return
	}
	a.wireDispatchMu.Lock()
	a.mu.Lock()
	stdin, cmd := a.stdin, a.cmd
	a.started, a.stdin, a.cmd, a.scanner = false, nil, nil, nil
	a.wireEpoch++
	a.mu.Unlock()
	a.wireDispatchMu.Unlock()
	stopACPProcess(stdin, cmd)
	a.failAppServerActiveTurns("ACP runtime stopped for recovery")
	a.failPendingRequests("ACP runtime stopped for recovery")
}
