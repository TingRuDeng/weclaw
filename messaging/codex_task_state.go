package messaging

import "github.com/fastclaw-ai/weclaw/agent"

type codexTaskPhase string

const (
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
	return t.runtimeOwner == agent.CodexOwnerDesktopLive || t.runtimeOwner == agent.CodexOwnerWeClawRuntime
}

// syncCodexRuntime 保存当前任务实际绑定的 owner 快照。
func (t *activeAgentTask) syncCodexRuntime(binding agent.CodexThreadBinding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.runtimeOwner = binding.Owner
	t.ownerRevision = binding.OwnerRevision
	t.codexThreadID = binding.Ref.ThreadID
}

func (t *activeAgentTask) markCodexDisconnected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase == codexTaskTerminal || t.phase == codexTaskStopping {
		return
	}
	t.phase = codexTaskDisconnected
	t.runtimeOwner = agent.CodexOwnerDesktopDisconnected
}

// markCodexObservationInterrupted 保存待核对 turn，并阻止控制命令沿用失效观察流。
func (t *activeAgentTask) markCodexObservationInterrupted(threadID string, turnID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase == codexTaskTerminal || t.phase == codexTaskStopping {
		return
	}
	t.phase = codexTaskDisconnected
	t.runtimeOwner = agent.CodexOwnerDesktopDisconnected
	t.codexThreadID = threadID
	t.codexTurnID = turnID
}

func (t *activeAgentTask) markCodexRunning(binding agent.CodexThreadBinding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.phase != codexTaskTerminal && t.phase != codexTaskStopping {
		t.phase = codexTaskRunning
		t.runtimeOwner = binding.Owner
		t.ownerRevision = binding.OwnerRevision
	}
}

func (t *activeAgentTask) refreshExternalCodexTurn(binding agent.CodexThreadBinding, turnID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.runtimeOwner = binding.Owner
	t.ownerRevision = binding.OwnerRevision
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
