package agent

import (
	"path/filepath"
	"testing"
	"time"
)

// TestPersistStateSerializesSnapshotAndWrite 验证保存锁覆盖完整持久化过程。
func TestPersistStateSerializesSnapshotAndWrite(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	a := NewACPAgent(ACPAgentConfig{Command: "mock", StateFile: stateFile})
	a.mu.Lock()
	a.threads["conversation-1"] = "thread-1"
	a.mu.Unlock()

	a.stateSaveMu.Lock()
	done := make(chan struct{})
	go func() {
		a.persistState()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("persistState bypassed active save lock")
	case <-time.After(50 * time.Millisecond):
	}
	a.stateSaveMu.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("persistState did not finish after save lock released")
	}
	persisted := readACPStateFile(t, stateFile)
	if persisted.Threads["conversation-1"] != "thread-1" {
		t.Fatalf("persisted threads=%#v, want latest thread", persisted.Threads)
	}
}
