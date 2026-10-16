package agent

import "fmt"

// prepareNativeCodexRestartLocked only fences this client's submissions. The
// official daemon and other frontends keep their independent lifetimes.
// Caller holds codexAdmissionMu; explicit Host termination uses the old path.
func (a *ACPAgent) prepareNativeCodexRestartLocked(persist func(CodexRestartSnapshot) error) (CodexRestartSnapshot, error) {
	gate := a.ensureCodexAppServerGate()
	if err := gate.beginExclusive(); err != nil {
		return CodexRestartSnapshot{}, err
	}
	snapshot := CodexRestartSnapshot{HostMode: codexHostModeNative}
	if err := persist(snapshot); err != nil {
		gate.finishExclusive(false, true)
		return CodexRestartSnapshot{}, fmt.Errorf("持久化 Codex 客户端重启意图: %w", err)
	}
	a.codexRestartMu.Lock()
	a.codexRestartSnapshot = snapshot
	a.codexRestartPrepared = true
	a.codexRestartMu.Unlock()
	gate.finishExclusive(true, false)
	return snapshot, nil
}
