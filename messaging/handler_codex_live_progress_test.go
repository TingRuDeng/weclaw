package messaging

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCodexDesktopDisconnectRebootsRolloutFromCurrentTail(t *testing.T) {
	h, path := activeRolloutHandoffFixture(t)
	before, _ := os.Stat(path)
	appendCodexRolloutRecord(t, path, rolloutProgressRecord("断线期间的新进展"))
	state, found, err := h.bootstrapCodexRolloutAfterDisconnect("thread-1", "turn-1")
	after, _ := os.Stat(path)
	if err != nil || !found || state.Offset != after.Size() || state.Offset <= before.Size() {
		t.Fatalf("state=%#v found=%v err=%v", state, found, err)
	}
	if state.Progress != "断线期间的新进展" {
		t.Fatalf("progress=%q", state.Progress)
	}
}

func TestCodexDesktopProgressSurvivesRolloutHandoff(t *testing.T) {
	h, path := activeRolloutHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	progress := make(chan string, 2)
	result := make(chan codexExternalWatchResult, 1)
	go func() {
		result <- h.watchCodexAfterDesktopDisconnect(ctx, externalCodexWatchRequest{
			threadID: "thread-1", turnID: "turn-1", onProgress: func(text string) { progress <- text },
		})
	}()
	select {
	case got := <-progress:
		if got != "切换前进展" {
			t.Fatalf("progress=%q", got)
		}
	case <-ctx.Done():
		t.Fatal("handoff 未保留最新进展")
	}
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-1", "最终结果"))
	if got := <-result; !got.Terminal || got.Final != "最终结果" {
		t.Fatalf("result=%#v", got)
	}
}

func TestCodexDesktopTerminalDeliveredOnce(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{})
	_, _, first := h.claimAndCompleteActiveTask("task-1", task)
	_, _, second := h.claimAndCompleteActiveTask("task-1", task)
	if !first || second {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

func TestCodexRolloutTurnReplacementEndsSupervisor(t *testing.T) {
	h, path := activeRolloutHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan codexExternalWatchResult, 1)
	go func() {
		result <- h.watchCodexAfterDesktopDisconnect(ctx, externalCodexWatchRequest{
			threadID: "thread-1", turnID: "turn-1",
		})
	}()
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-2"))
	select {
	case got := <-result:
		if !got.Terminal || !got.Failed {
			t.Fatalf("result=%#v", got)
		}
	case <-ctx.Done():
		t.Fatal("新 turn 替换后 supervisor 仍在等待旧 turn")
	}
}

func activeRolloutHandoffFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-1", workspace, "会话", "2026-07-11T09:00:00Z")
	path := localRolloutPathForTest(codexDir, "thread-1")
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-1"))
	appendCodexRolloutRecord(t, path, rolloutProgressRecord("切换前进展"))
	h.SetCodexLocalSessionDir(codexDir)
	return h, path
}
