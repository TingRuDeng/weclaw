package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestChatLegacyACPSendsSessionCancelWhenContextCancelled(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	a := NewACPAgent(ACPAgentConfig{Command: "claude-agent-acp"})
	var out bytes.Buffer
	a.stdin = nopWriteCloser{Buffer: &out}
	promptStarted := make(chan struct{})
	a.rpcCall = func(ctx context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "session/new":
			return json.RawMessage(`{"sessionId":"session-1"}`), nil
		case "session/prompt":
			close(promptStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	if _, err := a.createSession(ctx, "conversation-1"); err != nil {
		t.Fatalf("createSession error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := a.chatLegacyACP(ctx, "conversation-1", "hello", nil)
		done <- err
	}()
	<-promptStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("chat error=%v, want context canceled", err)
	}
	if got := out.String(); !strings.Contains(got, `"method":"session/cancel"`) || !strings.Contains(got, `"sessionId":"session-1"`) {
		t.Fatalf("cancel notification missing: %s", got)
	}
}

func TestChatCodexAppServerInterruptsTurnWhenContextCancelled(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir()})
	turnStarted := make(chan struct{})
	interruptCalled := make(chan struct{}, 1)
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			close(turnStarted)
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		case "turn/interrupt":
			p := params.(map[string]interface{})
			if p["threadId"] != "thread-1" || p["turnId"] != "turn-1" {
				return nil, fmt.Errorf("interrupt params=%#v", p)
			}
			interruptCalled <- struct{}{}
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	createCodexThreadForTest(t, ctx, a, "conversation-1")

	done := make(chan error, 1)
	go func() {
		_, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "conversation-1", message: "hello"})
		done <- err
	}()
	<-turnStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("chat error=%v, want context canceled", err)
	}
	select {
	case <-interruptCalled:
	default:
		t.Fatal("turn/interrupt was not called")
	}
}

func TestChatCodexAppServerReturnsStructuredInterruptedTurn(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir()})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}
	createCodexThreadForTest(t, context.Background(), a, "conversation-1")
	done := make(chan error, 1)
	go func() {
		_, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: context.Background(), conversationID: "conversation-1", message: "hello"})
		done <- err
	}()
	waitForCodexTurnChannel(t, a, "thread-1")
	a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted"}}`))

	err := <-done
	var interrupted *CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("chat error=%v, want CodexTurnInterruptedError", err)
	}
	if interrupted.ThreadID != "thread-1" || interrupted.TurnID != "turn-1" {
		t.Fatalf("interrupted=%#v, want thread-1 turn-1", interrupted)
	}
}

func waitForCodexTurnChannel(t *testing.T, a *ACPAgent, threadID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.notifyMu.Lock()
		turnCh := a.turnCh[threadID]
		a.notifyMu.Unlock()
		if turnCh != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("thread %s turn channel was not registered", threadID)
}
