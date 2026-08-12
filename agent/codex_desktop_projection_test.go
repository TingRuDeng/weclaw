package agent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCodexDesktopProjectionFindsExplicitActiveTurn(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", nil),
	})

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if !update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "turn-1" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
	if update.Snapshot.State.Model != "gpt-test" || update.Snapshot.State.Effort != "high" {
		t.Fatalf("model state = %#v", update.Snapshot.State)
	}
	assertCodexDesktopEvent(t, update.Events, "started", "turn-1")
}

func TestCodexDesktopProjectionDoesNotTreatUnknownStatusAsActive(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "mystery", nil),
	})

	update, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
	if len(update.Events) != 0 {
		t.Fatalf("events = %#v", update.Events)
	}
}

func TestCodexDesktopProjectionDoesNotEmitEmptyAgentText(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	items := []any{map[string]any{
		"id": "item-1", "type": "agentMessage", "status": "inProgress", "text": "",
	}}
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", items),
	})
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	if len(update.Events) != 1 || update.Events[0].Kind != "started" {
		t.Fatalf("events = %#v", update.Events)
	}
}

func TestCodexDesktopProjectionEmitsTextSuffixOnly(t *testing.T) {
	store := desktopProjectionStoreWithAgentText(t, "Hello")
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "items", 0, "text"}, Value: "Hello world"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	event := assertCodexDesktopEvent(t, update.Events, "", "turn-1")
	if event.ItemID != "item-1" || event.Delta != " world" || event.Text != "" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCodexDesktopProjectionRebuildsRewrittenText(t *testing.T) {
	store := desktopProjectionStoreWithAgentText(t, "Hello")
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "items", 0, "text"}, Value: "Rewritten"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	event := assertCodexDesktopEvent(t, update.Events, "", "turn-1")
	if event.ItemID != "item-1" || event.Delta != "" || event.Text != "Rewritten" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCodexDesktopProjectionKeepsStatusOnlyCompletedPendingAcrossOrdinaryRevisions(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", nil),
	})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "status"}, Value: "completed"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	if len(update.Events) != 0 {
		t.Fatalf("status-only completed must wait for final answer, events=%#v", update.Events)
	}

	for revision := uint64(3); revision <= 4; revision++ {
		update, err = store.applySnapshot(codexDesktopSnapshotSpec{
			threadID: "thread-1", epoch: 1, revision: revision, raw: update.Snapshot.Raw,
		})
		if err != nil {
			t.Fatalf("applySnapshot(revision=%d) error = %v", revision, err)
		}
		if len(update.Events) != 0 {
			t.Fatalf("status-only completed emitted at revision %d: %#v", revision, update.Events)
		}
		if !store.awaitingFinalAnswer("thread-1", "turn-1") {
			t.Fatalf("revision %d lost the pending final-answer barrier", revision)
		}
	}
}

func TestCodexDesktopProjectionWaitsForLateFinalAnswer(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "inProgress", nil),
	})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	statusOnly, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2,
		patches: []codexDesktopPatch{{Op: "replace", Path: []any{"turns", 0, "status"}, Value: "completed"}},
	})
	if err != nil {
		t.Fatalf("applyPatchSet(status) error = %v", err)
	}
	if len(statusOnly.Events) != 0 {
		t.Fatalf("status-only completed emitted early events=%#v", statusOnly.Events)
	}
	withFinal, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 2, revision: 3,
		patches: []codexDesktopPatch{{
			Op: "add", Path: []any{"turns", 0, "items", 0},
			Value: map[string]any{"id": "final-1", "type": "agentMessage", "phase": "final_answer", "text": "最终回答"},
		}},
	})
	if err != nil {
		t.Fatalf("applyPatchSet(final) error = %v", err)
	}
	if len(withFinal.Events) != 2 || withFinal.Events[0].Kind != "item_completed" || withFinal.Events[0].Text != "最终回答" || withFinal.Events[1].Kind != "completed" {
		t.Fatalf("events=%#v, want final_answer then completed", withFinal.Events)
	}
}

func TestCodexDesktopProjectionProjectsStatuslessCommentaryAsCompletedMessage(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", nil)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	}); err != nil {
		t.Fatal(err)
	}
	withCommentary := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", []any{
		map[string]any{"id": "commentary-1", "type": "agentMessage", "phase": "commentary", "text": "正在核对提交状态。"},
	})})
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: withCommentary,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := assertCodexDesktopEvent(t, update.Events, "item_completed", "turn-1")
	if event.ItemID != "commentary-1" || event.MessagePhase != "commentary" || event.Text != "正在核对提交状态。" {
		t.Fatalf("commentary event=%#v", event)
	}
}

func TestCodexDesktopProjectionTreatsUnphasedCompletedMessageAsLegacyFinal(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", nil)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: raw,
	}); err != nil {
		t.Fatal(err)
	}
	completed := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "completed", []any{
		map[string]any{"id": "legacy-final", "type": "agentMessage", "status": "completed", "text": "兼容最终回答"},
	})})
	update, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: completed,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCodexDesktopEvent(t, update.Events, "completed", "turn-1")
}

func TestCodexDesktopProjectionKeepsParallelSiblingTurnsSeparate(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	raw := desktopProjectionFixture("thread-1", []any{
		desktopTurnFixture("turn-1", "running", []any{
			map[string]any{"id": "final-1", "type": "agentMessage", "status": "completed", "phase": "final_answer", "text": "完成"},
		}),
		desktopTurnFixture("turn-2", "processing", nil),
	})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "status"}, Value: "completed"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	assertCodexDesktopEvent(t, update.Events, "completed", "turn-1")
	if !update.Snapshot.State.Active || update.Snapshot.State.ActiveTurnID != "turn-2" {
		t.Fatalf("state = %#v", update.Snapshot.State)
	}
}

func TestCodexDesktopProjectionEmitsAgentMessageAndHiddenCommandActivity(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	items := []any{
		map[string]any{"id": "agent-1", "type": "agentMessage", "status": "inProgress", "text": "Done"},
		map[string]any{"id": "command-1", "type": "commandExecution", "status": "inProgress", "command": []any{"git", "status"}, "aggregatedOutput": "private output"},
	}
	raw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "running", items)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	update, err := store.applyPatchSet(codexDesktopPatchSetSpec{
		threadID: "thread-1", epoch: 1, baseRevision: 1, revision: 2, patches: []codexDesktopPatch{
			{Op: "replace", Path: []any{"turns", 0, "items", 0, "status"}, Value: "completed"},
			{Op: "replace", Path: []any{"turns", 0, "items", 1, "status"}, Value: "completed"},
		}})
	if err != nil {
		t.Fatalf("applyPatchSet() error = %v", err)
	}
	event := assertCodexDesktopEvent(t, update.Events, "item_completed", "turn-1")
	if event.ItemID != "agent-1" || event.Text != "Done" {
		t.Fatalf("event = %#v", event)
	}
	if len(update.Events) != 2 || update.Events[1].Kind != "activity" || update.Events[1].ItemID != "command-1" ||
		update.Events[1].Progress != nil || update.Events[1].Text != "" {
		t.Fatalf("events = %#v, want completed agentMessage and non-display command activity", update.Events)
	}
}

func TestCodexDesktopProjectionDoesNotEmitSyntheticItemProgress(t *testing.T) {
	previousRaw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "running", nil)})
	_, _, previous, _ := projectCodexDesktopSnapshot("thread-1", previousRaw, nil)
	currentRaw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "running", []any{
		map[string]any{"id": "command-1", "type": "commandExecution", "status": "inProgress", "command": []any{"go", "test", "./..."}},
		map[string]any{"id": "file-1", "type": "fileChange", "status": "inProgress", "changes": []any{map[string]any{"path": "/private/workspace/secret.go"}}},
	})})
	_, _, _, events := projectCodexDesktopSnapshot("thread-1", currentRaw, &previous)
	for _, event := range events {
		if event.Kind == "progress" {
			t.Fatalf("synthetic Desktop item leaked into progress: %#v", event)
		}
	}
}

func TestCollectCodexDesktopTurnEmitsCompletedMessageAsNativeProgress(t *testing.T) {
	assembler := newCodexFinalAssembler()
	var progress []ProgressEvent
	collectCodexTurnText(
		assembler,
		&codexTurnEvent{
			Kind: "item_completed", ItemID: "message-1", MessagePhase: "commentary", Text: "我先读取当前实现。",
		},
		progressCallbacks{onEvent: func(event ProgressEvent) { progress = append(progress, event) }},
		newCodexTurnDiagnostics(codexTurnDiagnosticsLimit),
		&codexMessageProgressBuffer{},
	)
	if assembler.finalText() != "" {
		t.Fatalf("commentary leaked into final text: %q", assembler.finalText())
	}
	if len(progress) != 1 || progress[0].Kind != ProgressKindCommentary || progress[0].Text != "我先读取当前实现。" {
		t.Fatalf("progress=%#v", progress)
	}
}

func TestCollectCodexDesktopTurnReturnsFinalAnswerAfterCommentary(t *testing.T) {
	assembler := newCodexFinalAssembler()
	var progress []ProgressEvent
	callbacks := progressCallbacks{onEvent: func(event ProgressEvent) { progress = append(progress, event) }}
	diagnostics := newCodexTurnDiagnostics(codexTurnDiagnosticsLimit)
	buffer := &codexMessageProgressBuffer{}
	collectCodexTurnText(assembler, &codexTurnEvent{
		Kind: "item_completed", ItemID: "commentary-1", MessagePhase: "commentary", Text: "正在核对提交状态。",
	}, callbacks, diagnostics, buffer)
	collectCodexTurnText(assembler, &codexTurnEvent{
		Kind: "item_completed", ItemID: "final-1", MessagePhase: "final_answer", Text: "没有。当前工作区尚未提交和推送。",
	}, callbacks, diagnostics, buffer)

	if assembler.finalText() != "没有。当前工作区尚未提交和推送。" {
		t.Fatalf("final text=%q", assembler.finalText())
	}
	if len(progress) != 1 || progress[0].Text != "正在核对提交状态。" {
		t.Fatalf("progress=%#v", progress)
	}
}

func TestCollectCodexDesktopProjectionSeparatesStatuslessProgressAndFinalAnswer(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	active := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", nil)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 1, raw: active,
	}); err != nil {
		t.Fatal(err)
	}
	commentary := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", []any{
		map[string]any{"id": "commentary-1", "type": "agentMessage", "phase": "commentary", "text": "正在核对提交状态。"},
	})})
	commentaryUpdate, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 2, raw: commentary,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "completed", []any{
		map[string]any{"id": "commentary-1", "type": "agentMessage", "phase": "commentary", "text": "正在核对提交状态。"},
	})})
	statusUpdate, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 3, raw: completed,
	})
	if err != nil {
		t.Fatal(err)
	}
	ordinaryUpdate, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 4, raw: completed,
	})
	if err != nil {
		t.Fatal(err)
	}
	withFinal := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "completed", []any{
		map[string]any{"id": "commentary-1", "type": "agentMessage", "phase": "commentary", "text": "正在核对提交状态。"},
		map[string]any{"id": "final-1", "type": "agentMessage", "phase": "final_answer", "text": "没有。当前工作区尚未提交和推送。"},
	})})
	finalUpdate, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: 5, raw: withFinal,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := append([]*codexTurnEvent{}, commentaryUpdate.Events...)
	events = append(events, statusUpdate.Events...)
	events = append(events, ordinaryUpdate.Events...)
	events = append(events, finalUpdate.Events...)
	turnCh := make(chan *codexTurnEvent, len(events))
	for _, event := range events {
		turnCh <- event
	}
	var progress []ProgressEvent
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	reply, err := a.collectAttachedCodexTurn(context.Background(), codexThreadWatchOptions{
		conversationID: "conversation-1", threadID: "thread-1", targetTurnID: "turn-1", turnCh: turnCh,
		onProgressEvent: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "没有。当前工作区尚未提交和推送。" {
		t.Fatalf("reply=%q", reply)
	}
	if len(progress) != 1 || progress[0].Text != "正在核对提交状态。" {
		t.Fatalf("progress=%#v", progress)
	}
}

func TestCodexDesktopStateEvictsOnlyIdleThreads(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	active := desktopProjectionFixture("active", []any{desktopTurnFixture("turn-active", "running", nil)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "active", epoch: 1, revision: 1, raw: active}); err != nil {
		t.Fatalf("applySnapshot(active) error = %v", err)
	}
	for index := 0; index < codexDesktopMaxThreads; index++ {
		threadID := fmt.Sprintf("idle-%03d", index)
		raw := desktopProjectionFixture(threadID, nil)
		if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: threadID, epoch: 1, revision: 1, raw: raw}); err != nil {
			t.Fatalf("applySnapshot(%s) error = %v", threadID, err)
		}
	}
	if _, ok := store.snapshot("active"); !ok {
		t.Fatal("active thread 被淘汰")
	}
	if count := store.threadCount(); count != codexDesktopMaxThreads {
		t.Fatalf("thread count = %d", count)
	}
}

func desktopProjectionStoreWithAgentText(t *testing.T, text string) *codexDesktopStateStore {
	t.Helper()
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	items := []any{map[string]any{
		"id": "item-1", "type": "agentMessage", "status": "inProgress", "text": text,
	}}
	raw := desktopProjectionFixture("thread-1", []any{desktopTurnFixture("turn-1", "inProgress", items)})
	if _, err := store.applySnapshot(codexDesktopSnapshotSpec{threadID: "thread-1", epoch: 1, revision: 1, raw: raw}); err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
	return store
}

func desktopProjectionFixture(threadID string, turns []any) map[string]any {
	return map[string]any{
		"id": threadID, "turns": turns, "requests": []any{},
		"latestModel": "gpt-test", "latestReasoningEffort": "high",
		"threadRuntimeStatus": map[string]any{"type": "idle"},
	}
}

func desktopTurnFixture(turnID string, status string, items []any) map[string]any {
	return map[string]any{"turnId": turnID, "status": status, "items": items}
}

func assertCodexDesktopEvent(t *testing.T, events []*codexTurnEvent, kind string, turnID string) *codexTurnEvent {
	t.Helper()
	for _, event := range events {
		if event.Kind == kind && event.TurnID == turnID {
			return event
		}
	}
	t.Fatalf("event kind=%q turn=%q not found in %#v", kind, turnID, events)
	return nil
}
