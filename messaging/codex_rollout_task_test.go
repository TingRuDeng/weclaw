package messaging

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadCodexRolloutTaskStateFindsLatestActiveTurn(t *testing.T) {
	path := createCodexRolloutForTest(t)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-old"))
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-old", "旧结果"))
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-active"))
	appendCodexRolloutRecord(t, path, rolloutUserMessageRecord("turn-active", "当前本地任务"))
	appendCodexRolloutRecord(t, path, rolloutProgressRecord("当前最新进展"))

	state, err := readCodexRolloutTaskState(path)

	if err != nil {
		t.Fatalf("readCodexRolloutTaskState error: %v", err)
	}
	if !state.Active || state.TurnID != "turn-active" {
		t.Fatalf("state=%#v, want active latest turn", state)
	}
	if state.Preview != "当前本地任务" || state.Progress != "当前最新进展" {
		t.Fatalf("state=%#v, want task preview and latest progress", state)
	}
	if state.Offset <= 0 {
		t.Fatalf("offset=%d, want end-of-file offset", state.Offset)
	}
}

func TestReadCodexRolloutTaskStateDoesNotReviveCompletedTurn(t *testing.T) {
	path := createCodexRolloutForTest(t)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-complete"))
	appendCodexRolloutRecord(t, path, rolloutUserMessageRecord("turn-complete", "已经完成的任务"))
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-complete", "完成结果"))

	state, err := readCodexRolloutTaskState(path)

	if err != nil {
		t.Fatalf("readCodexRolloutTaskState error: %v", err)
	}
	if state.Active || state.Final != "完成结果" {
		t.Fatalf("state=%#v, want completed state", state)
	}
}

func TestWatchCodexRolloutTaskStopsOnAbort(t *testing.T) {
	path := createCodexRolloutForTest(t)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-abort"))
	state, err := readCodexRolloutTaskState(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, watchErr := watchCodexRolloutTask(ctx, state, nil)
		result <- watchErr
	}()
	appendCodexRolloutRecord(t, path, rolloutTurnAbortedRecord("turn-abort"))

	select {
	case watchErr := <-result:
		if watchErr == nil || !strings.Contains(watchErr.Error(), "已中断") {
			t.Fatalf("watch error=%v, want explicit abort", watchErr)
		}
	case <-ctx.Done():
		t.Fatal("watcher did not stop after turn_aborted")
	}
}

func TestReadCodexRolloutEventsKeepsOffsetForPartialLine(t *testing.T) {
	path := createCodexRolloutForTest(t)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-partial"))
	state, err := readCodexRolloutTaskState(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	appendCodexRolloutBytes(t, path, []byte(`{"type":"event_msg","payload":{"type":"agent_message"`))

	called := false
	next, err := readCodexRolloutEvents(path, state.Offset, func(codexRolloutEvent) error {
		called = true
		return nil
	})
	if err != nil || called || next != state.Offset {
		t.Fatalf("next=%d called=%v err=%v, partial line must stay unread", next, called, err)
	}
	appendCodexRolloutBytes(t, path, []byte(`,"message":"后续进展","phase":"commentary"}}`+"\n"))
	var progress string
	next, err = readCodexRolloutEvents(path, state.Offset, func(event codexRolloutEvent) error {
		progress = event.Text
		return nil
	})
	if err != nil || next <= state.Offset || progress != "后续进展" {
		t.Fatalf("next=%d progress=%q err=%v, completed line must be consumed", next, progress, err)
	}
}

func TestWatchCodexRolloutTaskRejectsNewTurnBeforeCompletion(t *testing.T) {
	path := createCodexRolloutForTest(t)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-current"))
	state, err := readCodexRolloutTaskState(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, watchErr := watchCodexRolloutTask(ctx, state, nil)
		result <- watchErr
	}()
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-next"))

	select {
	case watchErr := <-result:
		if watchErr == nil || !strings.Contains(watchErr.Error(), "新 turn") {
			t.Fatalf("watch error=%v, want explicit turn switch error", watchErr)
		}
	case <-ctx.Done():
		t.Fatal("watcher did not stop after a new turn started")
	}
}

func createCodexRolloutForTest(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/rollout-thread.jsonl"
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create rollout: %v", err)
	}
	return path
}

func appendCodexRolloutBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open rollout: %v", err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		t.Fatalf("append rollout bytes: %v", err)
	}
}

func rolloutTurnAbortedRecord(turnID string) map[string]any {
	return map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "turn_aborted", "turn_id": turnID, "reason": "interrupted",
	}}
}
