package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

const codexGateTestTimeout = 300 * time.Millisecond

func TestCodexAppServerGateDrainsBeforeRestart(t *testing.T) {
	gate := newCodexAppServerGate()
	permit, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var restarts atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- gate.drain(context.Background(), func(context.Context) error {
			restarts.Add(1)
			return nil
		})
	}()
	waitForCodexGateState(t, gate, codexAppServerDraining)
	if restarts.Load() != 0 {
		t.Fatal("active turn 释放前不应重启")
	}

	permit.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(codexGateTestTimeout):
		t.Fatal("drain 未在 active turn 结束后完成")
	}
	if restarts.Load() != 1 || gate.generation() != 2 {
		t.Fatalf("restarts=%d generation=%d", restarts.Load(), gate.generation())
	}
}

func TestCodexAppServerGateTimeoutKeepsRuntimeRunning(t *testing.T) {
	gate := newCodexAppServerGate()
	permit, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer permit.release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var restarts atomic.Int32

	err = gate.drain(ctx, func(context.Context) error {
		restarts.Add(1)
		return nil
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v，want deadline exceeded", err)
	}
	if restarts.Load() != 0 || gate.stateSnapshot() != codexAppServerRunning || gate.generation() != 1 {
		t.Fatalf("restarts=%d state=%s generation=%d", restarts.Load(), gate.stateSnapshot(), gate.generation())
	}
}

func TestCodexAppServerGateRestartFailureFailsClosed(t *testing.T) {
	gate := newCodexAppServerGate()
	restartErr := errors.New("restart failed")
	if err := gate.drain(context.Background(), func(context.Context) error {
		return restartErr
	}); !errors.Is(err, restartErr) {
		t.Fatalf("drain() error=%v", err)
	}
	if gate.stateSnapshot() != codexAppServerFailed || gate.generation() != 1 {
		t.Fatalf("state=%s generation=%d", gate.stateSnapshot(), gate.generation())
	}
	if _, err := gate.acquire(context.Background()); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("acquire() error=%v", err)
	}
}

func TestCodexAppServerGateExclusiveIsNonWaitingAndCanFailClosed(t *testing.T) {
	gate := newCodexAppServerGate()
	permit, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.beginExclusive(); !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("beginExclusive() error=%v", err)
	}
	permit.release()
	if err := gate.beginExclusive(); err != nil {
		t.Fatal(err)
	}
	gate.finishExclusive(false, false)
	if gate.stateSnapshot() != codexAppServerFailed {
		t.Fatalf("state=%s", gate.stateSnapshot())
	}
	if _, err := gate.acquire(context.Background()); !errors.Is(err, ErrCodexRuntimeUnavailable) {
		t.Fatalf("acquire failed gate error=%v", err)
	}
}

func waitForCodexGateState(t *testing.T, gate *codexAppServerGate, want codexAppServerGateState) {
	t.Helper()
	deadline := time.Now().Add(codexGateTestTimeout)
	for time.Now().Before(deadline) {
		if gate.stateSnapshot() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gate state=%s，want %s", gate.stateSnapshot(), want)
}
