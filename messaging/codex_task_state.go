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

func (t *activeAgentTask) replaceCodexThread(previousThreadID string, currentThreadID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.codexThreadID == previousThreadID {
		t.codexThreadID = currentThreadID
	}
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
	t.runtimeOwner = binding.Runtime
	t.ownerRevision = binding.Control.Revision
	t.codexTurnID = turnID
}

func (t *activeAgentTask) canControlExternalCodex() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.canControlExternalCodexLocked()
}

func (t *activeAgentTask) isStopping() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.phase == codexTaskStopping
}
