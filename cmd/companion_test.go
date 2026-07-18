package cmd

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCreateCompanionRuntimeRejectsCodexSecondWriter(t *testing.T) {
	runtime, err := createCompanionRuntime(agent.CompanionEndpoint{Agent: "codex"})
	if err == nil || !strings.Contains(err.Error(), "单一共享 app-server") {
		t.Fatalf("createCompanionRuntime() runtime=%#v error=%v, want shared-host rejection", runtime, err)
	}
}

func TestWaitForLiveCompanionEndpointRemovesStaleAndRetries(t *testing.T) {
	stale := agent.CompanionEndpoint{Agent: "codex", Host: "127.0.0.1", Port: 11111, Cwd: "/tmp/work"}
	live := agent.CompanionEndpoint{Agent: "codex", Host: "127.0.0.1", Port: 22222, Cwd: "/tmp/work"}
	readCount := 0
	removeCount := 0
	dialCount := 0

	endpoint, err := waitForLiveCompanionEndpoint(context.Background(), "codex", "/tmp/work", companionEndpointWaitOptions{
		Timeout:  time.Second,
		Interval: time.Millisecond,
		ReadEndpoint: func(string, string) (agent.CompanionEndpoint, error) {
			readCount++
			if readCount == 1 {
				return stale, nil
			}
			return live, nil
		},
		RemoveEndpoint: func(string, string) {
			removeCount++
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialCount++
			if dialCount == 1 {
				return nil, errors.New("connection refused")
			}
			left, right := net.Pipe()
			t.Cleanup(func() {
				_ = left.Close()
				_ = right.Close()
			})
			return left, nil
		},
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("waitForLiveCompanionEndpoint() error = %v", err)
	}
	if endpoint.Port != live.Port {
		t.Fatalf("endpoint port = %d, want %d", endpoint.Port, live.Port)
	}
	if removeCount != 1 {
		t.Fatalf("removeCount = %d, want 1", removeCount)
	}
	if readCount != 2 || dialCount != 2 {
		t.Fatalf("readCount=%d dialCount=%d, want 2/2", readCount, dialCount)
	}
}
