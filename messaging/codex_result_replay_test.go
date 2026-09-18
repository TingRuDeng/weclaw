package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexIdleRuntimeResultSurvivesEmptyRollout(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	f := newCodexSessionBindingFixture(t)
	state := agent.CodexThreadState{ThreadID: "thread-b", LastTurnID: "turn-result", LastTurnStatus: "completed", LastAgentMessageText: "完整结果"}
	f.ag.setBindingState(state)
	resolved := f.h.resolveExternalCodexTask(externalCodexTaskOptions{
		ctx: context.Background(), agent: f.ag, threadID: "thread-b",
	})
	if resolved.err != nil || resolved.state.LastAgentMessageText != state.LastAgentMessageText {
		t.Fatalf("idle runtime result lost: %+v", resolved)
	}
}

func TestCodexFeishuSelectionReplaysFullResult(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	f := newCodexSessionBindingFixture(t)
	f.h.ensureCodexSessions().SetFilePath(filepath.Join(t.TempDir(), "sessions.json"))
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a"}
	reply := newOutboxTestReplier(route)
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &codexFollowerTestPlatform{name: route.Platform, account: route.AccountID, reply: reply},
		Access:   platform.NewAccessControl([]string{f.routeUser}),
	}})
	f.h.SetPlatformRegistry(registry)
	outbox, err := newTerminalOutbox(filepath.Join(t.TempDir(), "outbox.json"), registry)
	if err != nil {
		t.Fatal(err)
	}
	f.h.terminalOutbox = outbox
	outbox.replayDelivered = f.h.commitCodexReplayDelivered
	outbox.deliveryDecision = func(entry *terminalOutboxEntry) (bool, string) {
		return f.h.terminalOutboxDeliveryDecision(registry, entry)
	}
	body := strings.Repeat("完整正文与列表\n", 180) + "```go\nfmt.Println(42)\n```\n[链接](https://example.com)"
	choose := func(thread string) {
		t.Helper()
		state := agent.CodexThreadState{ThreadID: thread, LastTurnID: "turn-" + thread, LastTurnStatus: "completed", LastAgentMessageText: body, LastTurnBodyLoaded: true}
		f.ag.setBindingState(state)
		f.ag.setThreadBinding(thread, agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: state})
		req := f.request(thread)
		req.platform, req.accountID, req.reply = route.Platform, route.AccountID, reply
		result, err := f.h.acquireCodexSessionWithBindingLocked(req)
		if err != nil {
			t.Fatal(err)
		}
		if !result.suppressLatestIdleResult {
			t.Fatal("history result must be separate from switch text")
		}
	}
	choose("thread-a")
	if len(outbox.entries) != 1 || outbox.entries[0].Text != body || !outbox.entries[0].RichResult {
		t.Fatalf("full rich replay was not queued: %+v", outbox.entries)
	}
	firstKey := outbox.entries[0].ID
	if err := outbox.attempt(context.Background(), firstKey, reply); err != nil {
		t.Fatal(err)
	}
	choose("thread-a")
	if len(outbox.entries) != 0 {
		t.Fatal("A -> A replayed")
	}
	choose("thread-b")
	choose("thread-a")
	if len(outbox.entries) != 2 || outbox.entries[1].ID == firstKey {
		t.Fatal("A -> B -> A must start a new replay")
	}
}

func TestCodexUnreadIdleResultIsNotEmptyResult(t *testing.T) {
	lines := renderLatestIdleCodexTaskResult(agent.CodexThreadState{LastTurnID: "turn-1", LastTurnStatus: "completed"})
	if text := strings.Join(lines, "\n"); !strings.Contains(text, "最近结果暂未读取成功") || strings.Contains(text, "没有返回文本") {
		t.Fatalf("unread result presented as empty: %q", text)
	}
}

type replayFixture struct {
	*codexSessionBindingFixture
	outbox   *terminalOutbox
	reply    *outboxTestReplier
	registry *platform.Registry
}

func newReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	f := newCodexSessionBindingFixture(t)
	f.h.ensureCodexSessions().SetFilePath(filepath.Join(t.TempDir(), "sessions.json"))
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a"}
	reply := newOutboxTestReplier(route)
	registry := platform.NewRegistry([]platform.RegistryEntry{{Platform: &codexFollowerTestPlatform{name: route.Platform, account: route.AccountID, reply: reply}, Access: platform.NewAccessControl([]string{f.routeUser})}})
	f.h.SetPlatformRegistry(registry)
	outbox, err := newTerminalOutbox(filepath.Join(t.TempDir(), "outbox.json"), registry)
	if err != nil {
		t.Fatal(err)
	}
	r := &replayFixture{f, outbox, reply, registry}
	r.attachOutbox()
	return r
}

func (f *replayFixture) attachOutbox() {
	f.h.terminalOutbox = f.outbox
	f.outbox.replayDelivered = f.h.commitCodexReplayDelivered
	f.outbox.deliveryBarrier = &f.h.codexFollowerDeliveryMu
	f.outbox.deliveryDecision = func(entry *terminalOutboxEntry) (bool, string) {
		return f.h.terminalOutboxDeliveryDecision(f.registry, entry)
	}
}

func (f *replayFixture) choose(t *testing.T, state agent.CodexThreadState) codexSessionAcquireResult {
	t.Helper()
	f.ag.setBindingState(state)
	f.ag.setThreadBinding(state.ThreadID, agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: state})
	req := f.request(state.ThreadID)
	req.platform, req.accountID, req.reply = platform.PlatformFeishu, "cli_a", f.reply
	result, err := f.h.acquireCodexSessionWithBindingLocked(req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func replayState(thread string) agent.CodexThreadState {
	return agent.CodexThreadState{ThreadID: thread, LastTurnID: "turn-" + thread, LastTurnStatus: "completed", LastAgentMessageText: "原文结果", LastTurnBodyLoaded: true}
}

func TestCodexReplayRetryAndRestartKeepDeliveryKey(t *testing.T) {
	f := newReplayFixture(t)
	state := replayState("thread-a")
	f.choose(t, state)
	id := f.outbox.entries[0].ID
	selection := f.h.ensureCodexSessions().remoteSelectionSnapshot(f.bindingKey, state.ThreadID).Binding.ResultReplay.ID
	f.reply.failResultAfterAccept = 1
	if err := f.outbox.attempt(context.Background(), id, f.reply); err == nil {
		t.Fatal("expected ambiguous delivery")
	}
	if got := f.h.ensureCodexSessions().remoteSelectionSnapshot(f.bindingKey, state.ThreadID).Binding.ResultReplay.PresentedTurnID; got != "" {
		t.Fatal("unknown send was marked presented")
	}
	path := f.h.ensureCodexSessions().filePath
	f.h.sessions.codex = newCodexSessionStore()
	f.h.ensureCodexSessions().SetFilePath(path)
	restarted, err := newTerminalOutbox(f.outbox.path, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	f.outbox = restarted
	f.attachOutbox()
	f.choose(t, state)
	if len(f.outbox.entries) != 1 || f.outbox.entries[0].ID != id {
		t.Fatal("restart changed pending delivery identity")
	}
	if err := f.outbox.attempt(context.Background(), id, f.reply); err != nil {
		t.Fatal(err)
	}
	if len(f.reply.results) != 1 || len(f.reply.resultKeys) != 2 || f.reply.resultKeys[0] != f.reply.resultKeys[1] {
		t.Fatal("ambiguous send was not retried idempotently")
	}
	f.h.sessions.codex = newCodexSessionStore()
	f.h.ensureCodexSessions().SetFilePath(path)
	f.choose(t, state)
	binding := f.h.ensureCodexSessions().remoteSelectionSnapshot(f.bindingKey, state.ThreadID).Binding
	if binding.ResultReplay.ID != selection || len(f.outbox.entries) != 0 {
		t.Fatal("delivered receipt did not survive restart")
	}
}

func TestCodexReplayUnreadAndTerminalStates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		state           agent.CodexThreadState
		want            string
		failed, stopped bool
	}{
		{name: "unread", state: agent.CodexThreadState{ThreadID: "thread-a", LastTurnID: "t", LastTurnStatus: "completed"}, want: "最近结果暂未读取成功"},
		{name: "empty", state: agent.CodexThreadState{ThreadID: "thread-a", LastTurnID: "t", LastTurnStatus: "completed", LastTurnBodyLoaded: true}, want: "最近任务已完成，未记录文本结果"},
		{name: "failed", state: agent.CodexThreadState{ThreadID: "thread-a", LastTurnID: "t", LastTurnStatus: "failed", LastTurnError: "真实失败原因", LastTurnBodyLoaded: true}, want: "真实失败原因", failed: true},
		{name: "stopped", state: agent.CodexThreadState{ThreadID: "thread-a", LastTurnID: "t", LastTurnStatus: "interrupted", LastAgentMessageText: "停止前的原文", LastTurnBodyLoaded: true}, want: "停止前的原文", stopped: true},
		{name: "no_turn", state: agent.CodexThreadState{ThreadID: "thread-a", LastTurnBodyLoaded: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplayFixture(t)
			result := f.choose(t, tc.state)
			if tc.name == "unread" {
				if !strings.Contains(result.latestIdleResultNotice, tc.want) || len(f.outbox.entries) != 0 {
					t.Fatal("unread body consumed replay")
				}
				loaded := tc.state
				loaded.LastTurnBodyLoaded = true
				loaded.LastAgentMessageText = "重试成功"
				f.choose(t, loaded)
				if len(f.outbox.entries) != 1 || f.outbox.entries[0].Text != "重试成功" {
					t.Fatal("unread replay could not recover")
				}
			} else if tc.name == "no_turn" {
				if len(f.outbox.entries) != 0 || result.latestIdleResultNotice != "" {
					t.Fatal("empty session fabricated a result")
				}
			} else if len(f.outbox.entries) != 1 || f.outbox.entries[0].Text != tc.want || f.outbox.entries[0].Failed != tc.failed || f.outbox.entries[0].Stopped != tc.stopped {
				t.Fatalf("wrong terminal replay: %+v", f.outbox.entries)
			}
		})
	}
}

func TestCodexReplaySwitchCancelsOldSelectionAndIgnoresLegacyClaims(t *testing.T) {
	f := newReplayFixture(t)
	state := replayState("thread-a")
	if _, err := f.h.ensureCodexSessions().claimPresentedResult(f.bindingKey, state.ThreadID, state.LastTurnID); err != nil {
		t.Fatal(err)
	}
	f.choose(t, state)
	if len(f.outbox.entries) != 1 {
		t.Fatal("legacy claim suppressed new explicit replay")
	}
	old := f.outbox.entries[0].ID
	f.choose(t, replayState("thread-b"))
	f.choose(t, state)
	for _, id := range []string{old, f.outbox.entries[1].ID} {
		if err := f.outbox.attempt(context.Background(), id, f.reply); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reply.results) != 0 || len(f.outbox.entries) != 1 {
		t.Fatal("stale selection delivered after switch")
	}
	if err := f.outbox.attempt(context.Background(), f.outbox.entries[0].ID, f.reply); err != nil {
		t.Fatal(err)
	}
	if len(f.reply.results) != 1 {
		t.Fatal("current selection did not deliver")
	}
}

func TestCodexReplayReceiptSaveFailureDoesNotClaimSuccess(t *testing.T) {
	f := newReplayFixture(t)
	state := replayState("thread-a")
	f.choose(t, state)
	id := f.outbox.entries[0].ID
	store := f.h.ensureCodexSessions()
	store.writeState = func(string, []byte) error { return errors.New("disk full") }
	if err := f.outbox.attempt(context.Background(), id, f.reply); err == nil {
		t.Fatal("receipt save error hidden")
	}
	if store.remoteSelectionSnapshot(f.bindingKey, state.ThreadID).Binding.ResultReplay.PresentedTurnID != "" || len(f.outbox.entries) != 1 {
		t.Fatal("failed receipt consumed replay")
	}
	store.writeState = nil
	if err := f.outbox.attempt(context.Background(), id, f.reply); err != nil {
		t.Fatal(err)
	}
	if len(f.reply.results) != 1 || f.reply.resultKeys[0] != f.reply.resultKeys[1] {
		t.Fatal("receipt retry duplicated result")
	}
}

func TestCodexReplayQueueSaveFailureCanRetry(t *testing.T) {
	f := newReplayFixture(t)
	original := f.outbox.path
	f.outbox.path = filepath.Join(original, "not-a-directory.json")
	if err := os.WriteFile(original, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	state := replayState("thread-a")
	result := f.choose(t, state)
	if result.latestIdleResultClaimErr == nil || len(f.outbox.entries) != 0 {
		t.Fatal("queue save failure was hidden")
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	f.outbox.path = original
	f.choose(t, state)
	if len(f.outbox.entries) != 1 {
		t.Fatal("queue save failure consumed selection")
	}
}

func TestCodexReplayObservedCompletionDoesNotDuplicateLiveResult(t *testing.T) {
	f := newReplayFixture(t)
	empty := agent.CodexThreadState{ThreadID: "thread-a", LastTurnBodyLoaded: true}
	f.choose(t, empty)
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok {
		t.Fatal("missing follower")
	}
	state := replayState("thread-a")
	if err := f.h.ensureCodexSessions().commitFollowerTurnPending(snapshot, state.LastTurnID); err != nil {
		t.Fatal(err)
	}
	f.choose(t, state)
	if len(f.outbox.entries) != 0 {
		t.Fatal("live observer completion was replayed")
	}
	f.choose(t, replayState("thread-b"))
	f.choose(t, state)
	if len(f.outbox.entries) != 2 {
		t.Fatal("returning to completed live task must replay")
	}
}

func TestCodexReplayDifferentWindowsHaveIndependentSelection(t *testing.T) {
	bindings := map[string]codexSessionBinding{}
	for _, key := range []string{"window-a", "window-b"} {
		selectCodexRemoteWorkspace(bindings, codexRemoteSelectionUpdate{BindingKey: key, WorkspaceRoot: "/workspace", TargetThreadID: "thread"}, time.Now())
	}
	if bindings["window-a"].ResultReplay.ID == bindings["window-b"].ResultReplay.ID {
		t.Fatal("windows shared replay selection")
	}
	a := bindings["window-a"].ResultReplay.ID
	selectCodexRemoteWorkspace(bindings, codexRemoteSelectionUpdate{BindingKey: "window-b", WorkspaceRoot: "/other", TargetThreadID: "other-thread"}, time.Now())
	if bindings["window-a"].ResultReplay.ID != a {
		t.Fatal("other window changed selection")
	}
}

func TestCodexReplayReadFailureKeepsBindingAndRetry(t *testing.T) {
	f := newReplayFixture(t)
	state := replayState("thread-a")
	f.choose(t, agent.CodexThreadState{ThreadID: "thread-a", LastTurnBodyLoaded: true})
	req := f.request("thread-a")
	result := codexSessionAcquireResult{route: req.route, syncErr: errors.New("history read failed")}
	result.resolution.Binding.State = state
	result = f.h.queueLatestIdleCodexReplay(result)
	if !strings.Contains(result.latestIdleResultNotice, "最近结果暂未读取成功") || len(f.outbox.entries) != 0 {
		t.Fatal("read error became empty result")
	}
	f.choose(t, state)
	if len(f.outbox.entries) != 1 {
		t.Fatal("read error consumed replay opportunity")
	}
}

func TestCodexCompleteEmptySnapshotDoesNotUseStaleBindingResult(t *testing.T) {
	result := codexSessionAcquireResult{externalState: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{ThreadID: "thread-a", LastTurnBodyLoaded: true}}}
	result.resolution.Binding.State = replayState("thread-a")
	if state := latestIdleCodexState(result); state.LastTurnID != "" || state.LastAgentMessageText != "" {
		t.Fatalf("complete empty history replaced by stale binding: %+v", state)
	}
}

func TestCodexReplayAndFollowerCatchupShareDeliveryResponsibility(t *testing.T) {
	f := newReplayFixture(t)
	f.choose(t, agent.CodexThreadState{ThreadID: "thread-a", LastTurnBodyLoaded: true})
	// 轻量基线未包含 turn，随后完整历史读取获得结果。
	state := replayState("thread-a")
	f.choose(t, state)
	if len(f.outbox.entries) != 1 {
		t.Fatal("missing replay")
	}
	snapshot, ok := f.h.ensureCodexSessions().followerSnapshot(f.bindingKey)
	if !ok {
		t.Fatal("missing follower")
	}
	if err := f.h.reconcileInactiveCodexFollower(snapshot, externalCodexTaskState{CodexThreadState: state}); err != nil {
		t.Fatal(err)
	}
	if len(f.outbox.entries) != 1 {
		t.Fatal("follower catchup duplicated pending replay")
	}
}
