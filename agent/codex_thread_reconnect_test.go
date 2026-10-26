package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func TestCodexThreadReadReconnectsAfterDisconnect(t *testing.T) {
	for _, operation := range []string{"validate", "state", "progress", "concurrent"} {
		t.Run(operation, func(t *testing.T) {
			a, socketPath := newCodexDaemonTestAgent(t)
			a.codexHostMode = codexHostModeNative
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			server := newFakeCodexHost(listener)
			server.threads["thread-reconnect"] = true
			server.start(t)
			a.codexDaemonMetadataCall = testCodexDaemonMetadata
			var actionsMu sync.Mutex
			var actions []string
			a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
				actionsMu.Lock()
				actions = append(actions, action)
				actionsMu.Unlock()
				return testCodexDaemonOutput("running", "pid", socketPath), nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.Start(ctx); err != nil {
				t.Fatal(err)
			}
			a.mu.Lock()
			a.threads["conversation-1"] = "thread-reconnect"
			a.mu.Unlock()
			disconnectCodexThreadTestClient(t, a)

			read := func() error {
				var state CodexThreadState
				var readErr error
				switch operation {
				case "validate", "concurrent":
					state, readErr = a.ValidateCodexThread(ctx, "conversation-1", "thread-reconnect")
				case "state":
					state, readErr = a.ReadCodexThreadState(ctx, "conversation-1", "thread-reconnect")
				case "progress":
					state, _, readErr = a.ReadCodexThreadProgressSnapshot(ctx, "conversation-1", "thread-reconnect")
				}
				if readErr != nil {
					return readErr
				}
				if state.ThreadID != "thread-reconnect" {
					return fmt.Errorf("read returned wrong thread: %#v", state)
				}
				return nil
			}
			workers := 1
			if operation == "concurrent" {
				workers = 8
			}
			results := make(chan error, workers)
			for range workers {
				go func() { results <- read() }()
			}
			for range workers {
				if err := <-results; err != nil {
					t.Errorf("read after disconnect must reconnect: %v", err)
				}
			}
			if got := server.connectionCount(); got != 2 {
				t.Errorf("connections=%d, want one initial connection and one reconnect", got)
			}
			actionsMu.Lock()
			defer actionsMu.Unlock()
			for _, action := range actions {
				if action != "version" {
					t.Errorf("read recovery changed daemon lifecycle: %s", action)
				}
			}
			if a.hostCmd != nil {
				t.Error("read recovery started a managed Host")
			}
			if threadID, ok := a.CurrentCodexThread("conversation-1"); !ok || threadID != "thread-reconnect" {
				t.Errorf("reconnect changed original binding: thread=%q exists=%t", threadID, ok)
			}
		})
	}
}

func TestCodexThreadReadReconnectPreservesIdentityFailure(t *testing.T) {
	a, socketPath := newCodexDaemonTestAgent(t)
	a.codexHostMode = codexHostModeNative
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := newFakeCodexHost(listener)
	server.start(t)
	a.codexDaemonMetadataCall = testCodexDaemonMetadata
	a.codexDaemonLifecycleCall = func(context.Context, string) (codexDaemonLifecycleOutput, error) {
		return testCodexDaemonOutput("running", "pid", socketPath), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	disconnectCodexThreadTestClient(t, a)
	wantErr := errors.New("daemon identity changed")
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		if action != "version" {
			t.Errorf("identity failure must not change daemon lifecycle: %s", action)
		}
		return codexDaemonLifecycleOutput{}, wantErr
	}
	if _, err := a.ValidateCodexThread(ctx, "conversation-1", "thread-reconnect"); !errors.Is(err, wantErr) {
		t.Fatalf("validation error=%v, want original identity failure %v", err, wantErr)
	}
	if a.isRuntimeStarted() || a.hostCmd != nil {
		t.Fatal("identity failure must leave client disconnected without starting a replacement Host")
	}
}

func disconnectCodexThreadTestClient(t *testing.T, a *ACPAgent) {
	t.Helper()
	a.mu.Lock()
	connection := a.stdin.(*codexWebSocketTransport).conn
	a.mu.Unlock()
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for a.isRuntimeStarted() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.isRuntimeStarted() {
		t.Fatal("read loop did not clear the disconnected client")
	}
}
