package agent

import (
	"io"
	"sync"
	"testing"
	"time"
)

type blockingACPWriteCloser struct {
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
}

func newBlockingACPWriteCloser() *blockingACPWriteCloser {
	return &blockingACPWriteCloser{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingACPWriteCloser) Write(_ []byte) (int, error) {
	w.enterOnce.Do(func() { close(w.entered) })
	<-w.release
	return 0, io.ErrClosedPipe
}

func (w *blockingACPWriteCloser) Close() error {
	w.closeOnce.Do(func() { close(w.release) })
	return nil
}

func TestACPAgentStopInterruptsBlockedWriteWithoutWaitingForStateLock(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "blocking-agent"})
	writer := newBlockingACPWriteCloser()
	a.mu.Lock()
	a.started = true
	a.stdin = writer
	a.wireEpoch = 1
	a.mu.Unlock()

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- a.writeJSONLine([]byte(`{"jsonrpc":"2.0"}`))
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("ACP write did not enter blocking writer")
	}

	stopDone := make(chan struct{})
	go func() {
		a.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		_ = writer.Close()
		<-stopDone
		t.Fatal("Stop waited for the blocked ACP write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked ACP write error = nil, want closed pipe")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked ACP write did not return after Stop closed stdin")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started || a.stdin != nil {
		t.Fatalf("runtime state after Stop: started=%t stdin=%v", a.started, a.stdin)
	}
}
