package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexInterruptedTurnContinuesFromSameRollout(t *testing.T) {
	h, path := activeRolloutHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan codexExternalWatchResult, 1)
	go func() {
		result <- h.reconcileInterruptedCodexTurn(ctx, &agent.CodexTurnInterruptedError{
			ThreadID: "thread-1", TurnID: "turn-1",
		}, nil)
	}()
	appendCodexRolloutRecord(t, path, rolloutProgressRecord("中断后的进展"))
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-1", "中断后正常完成"))
	if got := <-result; !got.Terminal || got.Failed || got.Final != "中断后正常完成" {
		t.Fatalf("result=%#v", got)
	}
}

func TestCodexAgentTaskUsesRolloutAfterInterruptedObservation(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	codexDir := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-1", route.workspaceRoot, "会话", "2026-07-14T00:00:00Z")
	path := localRolloutPathForTest(codexDir, "thread-1")
	h.SetCodexLocalSessionDir(codexDir)
	ag.err = &agent.CodexTurnInterruptedError{ThreadID: "thread-1", TurnID: "turn-1"}
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-1"))
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-1", "恢复后的最终结果"))
	waitUntil(t, func() bool {
		_, active := h.activeTask(route.conversationID)
		return !active
	})
	texts := opts.reply.(*platformtest.Replier).Texts
	if len(texts) != 1 || !containsText(texts, "恢复后的最终结果") || containsText(texts, "Codex turn 已中断") {
		t.Fatalf("texts=%#v", texts)
	}
}

func TestCodexInterruptedAgentTaskStopsWithoutFailureReply(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	codexDir := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-1", route.workspaceRoot, "会话", "2026-07-14T00:00:00Z")
	path := localRolloutPathForTest(codexDir, "thread-1")
	h.SetCodexLocalSessionDir(codexDir)
	ag.err = &agent.CodexTurnInterruptedError{ThreadID: "thread-1", TurnID: "turn-1"}
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-1"))
	waitUntil(t, func() bool {
		task, active := h.activeTask(route.conversationID)
		return active && taskPhase(task) == codexTaskDisconnected
	})
	if cancelled, denied := h.cancelActiveTask(route.conversationID, "user-1"); !cancelled || denied {
		t.Fatalf("cancelled=%v denied=%v", cancelled, denied)
	}
	waitUntil(t, func() bool {
		_, active := h.activeTask(route.conversationID)
		return !active
	})
	if texts := opts.reply.(*platformtest.Replier).Texts; len(texts) != 0 {
		t.Fatalf("texts=%#v，停止后不应补发中断失败", texts)
	}
}

func TestCodexInterruptedTurnKeepsWaitingForDelayedRollout(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	h.SetCodexLocalSessionDir(codexDir)
	writeLocalCodexSession(t, codexDir, "thread-late", t.TempDir(), "延迟会话", "2026-07-14T00:00:00Z")
	path := localRolloutPathForTest(codexDir, "thread-late")
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-old"))
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-old", "上一轮结果"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan codexExternalWatchResult, 1)
	go func() {
		result <- h.reconcileInterruptedCodexTurn(ctx, &agent.CodexTurnInterruptedError{
			ThreadID: "thread-late", TurnID: "turn-late",
		}, nil)
	}()
	time.Sleep(250 * time.Millisecond)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-late"))
	time.Sleep(250 * time.Millisecond)
	appendCodexRolloutRecord(t, path, rolloutTaskCompleteRecord("turn-late", "延迟写入后完成"))
	if got := <-result; got.Failed || got.Final != "延迟写入后完成" {
		t.Fatalf("result=%#v", got)
	}
}

func TestCodexInterruptedTurnReportsExplicitAbort(t *testing.T) {
	h, path := activeRolloutHandoffFixture(t)
	appendCodexRolloutRecord(t, path, rolloutTurnAbortedRecord("turn-1"))
	result := h.reconcileInterruptedCodexTurn(context.Background(), &agent.CodexTurnInterruptedError{
		ThreadID: "thread-1", TurnID: "turn-1",
	}, nil)
	if !result.Terminal || !result.Failed || !strings.Contains(result.Err.Error(), "interrupted") {
		t.Fatalf("result=%#v", result)
	}
}

func TestCodexInterruptedTurnRejectsReplacementTurn(t *testing.T) {
	h, path := activeRolloutHandoffFixture(t)
	appendCodexRolloutRecord(t, path, rolloutTaskStartedRecord("turn-2"))
	result := h.reconcileInterruptedCodexTurn(context.Background(), &agent.CodexTurnInterruptedError{
		ThreadID: "thread-1", TurnID: "turn-1",
	}, nil)
	if !result.Terminal || !result.Failed || !errors.Is(result.Err, errCodexRolloutTurnChanged) {
		t.Fatalf("result=%#v", result)
	}
}
