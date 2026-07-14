package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestWatchCodexThreadReadsInterruptedIdleState 验证注册时已空闲的中断 turn 不会返回旧文本。
func TestWatchCodexThreadReadsInterruptedIdleState(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}})
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[{"id":"turn-1","status":"interrupted","items":[{"id":"msg-1","type":"agentMessage","text":"部分旧文本"}]}]}}`), nil
	}

	_, err := a.WatchCodexThread(context.Background(), "conversation-1", "thread-1", nil)
	assertInterruptedTurnError(t, err, "thread-1", "turn-1")
}

// TestDesktopWatchReconcilesInterruptedState 验证漏收事件时 Desktop 权威状态仍返回中断终态。
func TestDesktopWatchReconcilesInterruptedState(t *testing.T) {
	a, _ := desktopRuntimeTestAgent(t)
	applyDesktopRuntimeTestState(t, a, 2, "inProgress", "")
	reconcile := make(chan time.Time, 1)
	errCh := make(chan error, 1)
	go func() {
		_, err := a.watchCodexThreadWithReconcile(context.Background(), codexThreadWatchOptions{
			conversationID: "conversation-1", threadID: "thread-1", reconcile: reconcile,
		})
		errCh <- err
	}()
	waitForDesktopTurnWatcher(t, a, "thread-1")
	applyDesktopRuntimeTestState(t, a, 3, "interrupted", "部分旧文本")
	reconcile <- time.Now()
	assertInterruptedTurnError(t, <-errCh, "thread-1", "turn-1")
}

// assertInterruptedTurnError 验证结构化中断错误保留目标身份。
func assertInterruptedTurnError(t *testing.T, err error, threadID string, turnID string) {
	t.Helper()
	var interrupted *CodexTurnInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("error=%v, want CodexTurnInterruptedError", err)
	}
	if interrupted.ThreadID != threadID || interrupted.TurnID != turnID {
		t.Fatalf("interrupted=%#v, want %s/%s", interrupted, threadID, turnID)
	}
}
