package messaging

import "github.com/fastclaw-ai/weclaw/agent"

type codexTaskPhase string

const (
	codexTaskReserved     codexTaskPhase = "reserved"
	codexTaskRunning      codexTaskPhase = "running"
	codexTaskStopping     codexTaskPhase = "stopping"
	codexTaskDisconnected codexTaskPhase = "disconnected"
	codexTaskTerminal     codexTaskPhase = "terminal"
)

// claimTerminal 确保多个观察源只能有一个进入任务终态。
func (t *activeAgentTask) claimTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.claimTerminalLocked()
}

func (t *activeAgentTask) claimTerminalLocked() bool {
	if t.phase == codexTaskTerminal {
		return false
	}
	t.stopRequested = false
	t.phase = codexTaskTerminal
	return true
}

func (t *activeAgentTask) isExternalCodexLocked() bool {
	return t.codexThreadID != "" && t.codexTurnID != ""
}

func (t *activeAgentTask) canControlExternalCodexLocked() bool {
	if !t.isExternalCodexLocked() || t.phase != codexTaskRunning {
		return false
	}
	return t.runtimeOwner == agent.CodexRuntimeWeClaw
}

// canResolveExternalCodexControlLocked 允许本进程已启动的 turn 在缓存 owner 尚未回填时
// 进入权威 runtime 探测；只读镜像、断线和已知非 WeClaw owner 仍保持不可控。
func (t *activeAgentTask) canResolveExternalCodexControlLocked() bool {
	if t.canControlExternalCodexLocked() {
		return true
	}
	return t.inProcessCodexLifecycle &&
		t.isExternalCodexLocked() &&
		t.phase == codexTaskRunning &&
		t.runtimeOwner == agent.CodexRuntimeUnknown
}

func (t *activeAgentTask) markCodexDisconnected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase == codexTaskTerminal || t.phase == codexTaskStopping {
		return
	}
	t.phase = codexTaskDisconnected
	t.runtimeOwner = agent.CodexRuntimeUnknown
}

// markCodexObservationInterrupted 保存待核对 turn，并阻止控制命令沿用失效观察流。
func (t *activeAgentTask) markCodexObservationInterrupted(threadID string, turnID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase == codexTaskTerminal || t.phase == codexTaskStopping {
		return
	}
	t.phase = codexTaskDisconnected
	t.runtimeOwner = agent.CodexRuntimeUnknown
	t.codexThreadID = threadID
	t.codexTurnID = turnID
}

func (t *activeAgentTask) replaceCodexThread(previousThreadID string, currentThreadID string) error {
	t.mu.Lock()
	if t.codexThreadID == previousThreadID {
		t.codexThreadID = currentThreadID
		t.trace = t.trace.WithThreadTurn(t.codexThreadID, t.codexTurnID)
	}
	progress := t.progress
	trace := t.trace
	t.mu.Unlock()
	if progress != nil {
		return progress.refreshActiveStreamRecoveryTrace(trace)
	}
	return nil
}

func (t *activeAgentTask) activeRecoveryReservationID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	progress := t.progress
	t.mu.Unlock()
	if progress == nil {
		return ""
	}
	return progress.activeRecoveryReservation()
}

func (t *activeAgentTask) markCodexRunning(binding agent.CodexThreadBinding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase != codexTaskTerminal && t.phase != codexTaskStopping {
		t.phase = codexTaskRunning
		t.runtimeOwner = binding.Runtime
		t.ownerRevision = binding.Control.Revision
	}
}

func (t *activeAgentTask) refreshExternalCodexTurn(binding agent.CodexThreadBinding, turnID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase != codexTaskRunning {
		return
	}
	t.runtimeOwner = binding.Runtime
	t.ownerRevision = binding.Control.Revision
	t.codexTurnID = turnID
}

func (t *activeAgentTask) canResolveExternalCodexControl() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canResolveExternalCodexControlLocked()
}

func (t *activeAgentTask) isStopping() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.phase == codexTaskStopping
}
