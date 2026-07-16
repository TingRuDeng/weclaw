package messaging

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type codexStoreIntentFixture struct {
	threadID       string
	owner          codexControlOwner
	bindingKey     string
	conversationID string
}
type codexStoreWriterProbe struct {
	calls    atomic.Int32
	err      error
	delegate codexSessionStateWriter
}
type codexRemoteStoreExpectation struct {
	bindingKey string
	active     string
	workspaces map[string]codexWorkspaceSession
	targetID   string
	target     codexControlIntent
	old        map[string]codexControlIntent
	writes     int
}

func TestCodexRemoteSelectionCommitSelectsTargetAndReleasesRouteOwnedThreads(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(stateFile)
	bindingKey := "feishu:window-a\x00codex"
	workspaceA, workspaceB, workspaceC := "/workspace/a", "/workspace/b", "/workspace/c"
	store.setActiveWorkspace(bindingKey, workspaceA)
	store.setThread(bindingKey, workspaceA, "thread-a")
	store.setThread(bindingKey, workspaceB, "thread-b")
	store.addCodexStoreWorkspace(bindingKey, workspaceC, "thread-b")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-a"})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-c", codexControlRemote, bindingKey, "conversation-c"})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-b", codexControlDesktop, "", ""})
	probe := &codexStoreWriterProbe{delegate: writeCodexSessionStateFile}
	store.writeState = probe.write
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-b")
	if ids := codexRemoteSelectionThreadIDs(snapshot); len(ids) != 3 || ids[0] != "thread-a" || ids[1] != "thread-b" || ids[2] != "thread-c" {
		t.Fatalf("thread IDs=%v", ids)
	}
	result, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspaceB, TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Target.Owner != codexControlRemote || len(result.Released) != 2 {
		t.Fatalf("result=%#v", result)
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, active: workspaceB, targetID: "thread-b", writes: 1,
		workspaces: map[string]codexWorkspaceSession{
			workspaceA: {ThreadID: "thread-a"}, workspaceB: {ThreadID: "thread-b"}, workspaceC: {},
		},
		target: codexControlIntent{Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-b", Revision: 2},
		old: map[string]codexControlIntent{
			"thread-a": {Owner: codexControlDesktop, Revision: 2},
			"thread-c": {Owner: codexControlDesktop, Revision: 2},
		},
	})
	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(stateFile)
	if got := reloaded.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("reloaded target=%#v", got)
	}
}
func TestCodexRemoteSelectionCommitKeepsLiveStateWhenWriteFails(t *testing.T) {
	store := newCodexSessionStore()
	store.SetFilePath(filepath.Join(t.TempDir(), "state.json"))
	bindingKey, workspaceA := "route-a\x00codex", "/workspace/a"
	store.setThread(bindingKey, workspaceA, "thread-a")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-a"})
	probe := &codexStoreWriterProbe{err: errors.New("写入失败")}
	store.writeState = probe.write
	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: store.remoteSelectionSnapshot(bindingKey, "thread-b"),
	})
	if err == nil {
		t.Fatal("候选状态写入失败时不应提交内存")
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, workspaces: map[string]codexWorkspaceSession{workspaceA: {ThreadID: "thread-a"}},
		targetID: "thread-b", target: codexControlIntent{Owner: codexControlUnclaimed},
		old: map[string]codexControlIntent{"thread-a": {Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-a", Revision: 1}}, writes: 1,
	})
}

func TestCodexRollbackRemoteSelectionRestoresCommittedState(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	bindingKey, workspaceA, workspaceB := "route-a\x00codex", "/workspace/a", "/workspace/b"
	store.setActiveWorkspace(bindingKey, workspaceA)
	store.setThread(bindingKey, workspaceA, "thread-a")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-a"})
	store.writeState = probe.write
	result, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspaceB, TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: store.remoteSelectionSnapshot(bindingKey, "thread-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.rollbackRemoteSelection(result); err != nil {
		t.Fatal(err)
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, active: workspaceA, targetID: "thread-b", writes: 2,
		workspaces: map[string]codexWorkspaceSession{workspaceA: {ThreadID: "thread-a"}},
		target:     codexControlIntent{Owner: codexControlUnclaimed},
		old: map[string]codexControlIntent{
			"thread-a": {Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-a", Revision: 1},
		},
	})
}

func TestCodexRollbackRemoteSelectionRejectsConcurrentState(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	bindingKey := "route-a\x00codex"
	result, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: store.remoteSelectionSnapshot(bindingKey, "thread-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.writeState = probe.write
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-b", codexControlRemote, bindingKey, "conversation-new"})
	if err := store.rollbackRemoteSelection(result); !errors.Is(err, errCodexRemoteSelectionChanged) {
		t.Fatalf("error=%v", err)
	}
	if got := store.controlIntent("thread-b"); got.ConversationID != "conversation-new" {
		t.Fatalf("回滚覆盖了并发状态: %#v", got)
	}
}

func TestCodexRemoteSelectionCommitRejectsStaleSnapshot(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	bindingKey, workspaceA := "route-a\x00codex", "/workspace/a"
	store.setThread(bindingKey, workspaceA, "thread-a")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-old"})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-b", codexControlDesktop, "", ""})
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-b")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-b", codexControlRemote, bindingKey, "conversation-new"})
	store.writeState = probe.write
	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-new", Expected: snapshot,
	})
	if !errors.Is(err, errCodexRemoteSelectionChanged) {
		t.Fatalf("error=%v", err)
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, workspaces: map[string]codexWorkspaceSession{workspaceA: {ThreadID: "thread-a"}},
		targetID: "thread-b", target: codexControlIntent{Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-new", Revision: 2},
		old: map[string]codexControlIntent{"thread-a": {Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-old", Revision: 1}},
	})
}
func TestCodexRemoteSelectionCommitIsIdempotent(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	bindingKey, workspace := "route-a\x00codex", "/workspace/b"
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-a"})
	update := codexRemoteSelectionUpdate{BindingKey: bindingKey, WorkspaceRoot: workspace, TargetThreadID: "thread-b", ConversationID: "conversation-b"}
	update.Expected = store.remoteSelectionSnapshot(bindingKey, "thread-b")
	store.writeState = probe.write
	if _, err := store.commitRemoteSelection(update); err != nil {
		t.Fatal(err)
	}
	update.Expected = store.remoteSelectionSnapshot(bindingKey, "thread-b")
	if _, err := store.commitRemoteSelection(update); err != nil {
		t.Fatal(err)
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, active: workspace, workspaces: map[string]codexWorkspaceSession{workspace: {ThreadID: "thread-b"}},
		targetID: "thread-b", target: codexControlIntent{Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-b", Revision: 1},
		old: map[string]codexControlIntent{"thread-a": {Owner: codexControlDesktop, Revision: 2}}, writes: 1,
	})
}
func TestCodexRemoteSelectionCommitRejectsOtherRoute(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	bindingKey, workspace := "route-a\x00codex", "/workspace/a"
	store.setThread(bindingKey, workspace, "thread-a")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-a"})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-b", codexControlRemote, "route-b\x00codex", "conversation-b"})
	store.writeState = probe.write
	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-a", Expected: store.remoteSelectionSnapshot(bindingKey, "thread-b"),
	})
	if !errors.Is(err, errCodexRemoteSelectionOtherRoute) {
		t.Fatalf("error=%v", err)
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, workspaces: map[string]codexWorkspaceSession{workspace: {ThreadID: "thread-a"}},
		targetID: "thread-b", target: codexControlIntent{Owner: codexControlRemote, RouteBindingKey: "route-b\x00codex", ConversationID: "conversation-b", Revision: 1},
		old: map[string]codexControlIntent{"thread-a": {Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-a", Revision: 1}},
	})
}
func TestCodexRemoteSelectionCommitRejectsOwnedSetChange(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	bindingKey, workspace := "route-a\x00codex", "/workspace/a"
	store.setThread(bindingKey, workspace, "thread-a")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, bindingKey, "conversation-a"})
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-b")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-c", codexControlRemote, bindingKey, "conversation-c"})
	store.writeState = probe.write
	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: snapshot,
	})
	if !errors.Is(err, errCodexRemoteSelectionChanged) {
		t.Fatalf("error=%v", err)
	}
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: bindingKey, workspaces: map[string]codexWorkspaceSession{workspace: {ThreadID: "thread-a"}},
		targetID: "thread-b", target: codexControlIntent{Owner: codexControlUnclaimed},
		old: map[string]codexControlIntent{
			"thread-a": {Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-a", Revision: 1},
			"thread-c": {Owner: codexControlRemote, RouteBindingKey: bindingKey, ConversationID: "conversation-c", Revision: 1},
		},
	})
}
func TestCodexRemoteSelectionConcurrentClaimHasSingleWinner(t *testing.T) {
	store, probe := newCodexRemoteTestStore(t)
	routeA, routeB := "route-a\x00codex", "route-b\x00codex"
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-a", codexControlRemote, routeA, "conversation-a"})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{"thread-c", codexControlRemote, routeB, "conversation-c"})
	updates := []codexRemoteSelectionUpdate{
		{BindingKey: routeA, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b", ConversationID: "conversation-a"},
		{BindingKey: routeB, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b", ConversationID: "conversation-b"},
	}
	for index := range updates {
		updates[index].Expected = store.remoteSelectionSnapshot(updates[index].BindingKey, "thread-b")
	}
	store.writeState = probe.write
	errorsByUpdate := commitCodexRemoteSelectionsConcurrently(store, updates)
	if (errorsByUpdate[0] == nil) == (errorsByUpdate[1] == nil) {
		t.Fatalf("errors=%v，必须仅一个成功", errorsByUpdate)
	}
	winner, loser := 0, 1
	if errorsByUpdate[1] == nil {
		winner, loser = 1, 0
	}
	winnerOld, loserOld, loserConversation := []string{"thread-a", "thread-c"}[winner], []string{"thread-a", "thread-c"}[loser], []string{"conversation-a", "conversation-c"}[loser]
	probe.assertCodexRemoteStore(t, store, codexRemoteStoreExpectation{
		bindingKey: updates[winner].BindingKey, active: "/workspace/b",
		workspaces: map[string]codexWorkspaceSession{"/workspace/b": {ThreadID: "thread-b"}},
		targetID:   "thread-b", target: codexControlIntent{Owner: codexControlRemote, RouteBindingKey: updates[winner].BindingKey, ConversationID: updates[winner].ConversationID, Revision: 1},
		old: map[string]codexControlIntent{
			winnerOld: {Owner: codexControlDesktop, Revision: 2},
			loserOld:  {Owner: codexControlRemote, RouteBindingKey: updates[loser].BindingKey, ConversationID: loserConversation, Revision: 1},
		}, writes: 1,
	})
}
func claimCodexStoreIntent(t *testing.T, store *codexSessionStore, fixture codexStoreIntentFixture) {
	t.Helper()
	current := store.controlIntent(fixture.threadID)
	_, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: fixture.threadID, Owner: fixture.owner,
		RouteBindingKey: fixture.bindingKey, ConversationID: fixture.conversationID,
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatalf("建立测试控制意图失败: %v", err)
	}
}
func (p *codexStoreWriterProbe) write(filePath string, data []byte) error {
	p.calls.Add(1)
	if p.err != nil {
		return p.err
	}
	if p.delegate != nil {
		return p.delegate(filePath, data)
	}
	return nil
}
func newCodexRemoteTestStore(t *testing.T) (*codexSessionStore, *codexStoreWriterProbe) {
	store := newCodexSessionStore()
	store.SetFilePath(filepath.Join(t.TempDir(), "codex-sessions.json"))
	return store, &codexStoreWriterProbe{}
}
func (s *codexSessionStore) addCodexStoreWorkspace(bindingKey string, workspace string, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.ensureBindingLocked(bindingKey)
	binding.Workspaces[workspace] = codexWorkspaceSession{ThreadID: threadID}
	s.bindings[bindingKey] = binding
}
func (p *codexStoreWriterProbe) assertCodexRemoteStore(t *testing.T, store *codexSessionStore, want codexRemoteStoreExpectation) {
	t.Helper()
	snapshot := store.remoteSelectionSnapshot(want.bindingKey, want.targetID)
	if snapshot.Binding.ActiveWorkspace != want.active || len(snapshot.Binding.Workspaces) != len(want.workspaces) {
		t.Fatalf("binding=%#v，want active=%q workspaces=%#v", snapshot.Binding, want.active, want.workspaces)
	}
	for workspace, session := range want.workspaces {
		got := snapshot.Binding.Workspaces[workspace]
		if got.ThreadID != session.ThreadID || got.PendingNewThread != session.PendingNewThread {
			t.Fatalf("workspace=%q session=%#v，want %#v", workspace, got, session)
		}
	}
	want.target.assertCodexIntent(t, "target", snapshot.Target)
	for threadID, intent := range want.old {
		intent.assertCodexIntent(t, threadID, store.controlIntent(threadID))
	}
	if got := int(p.calls.Load()); got != want.writes {
		t.Fatalf("writer calls=%d，want %d", got, want.writes)
	}
}
func (want codexControlIntent) assertCodexIntent(t *testing.T, label string, got codexControlIntent) {
	t.Helper()
	got.UpdatedAt, want.UpdatedAt = "", ""
	if got != want {
		t.Fatalf("%s intent=%#v，want %#v", label, got, want)
	}
}

type codexIndexedError struct {
	index int
	err   error
}

func commitCodexRemoteSelectionsConcurrently(store *codexSessionStore, updates []codexRemoteSelectionUpdate) []error {
	start := make(chan struct{})
	results := make(chan codexIndexedError, len(updates))
	for index, update := range updates {
		go func(index int, update codexRemoteSelectionUpdate) {
			<-start
			_, err := store.commitRemoteSelection(update)
			results <- codexIndexedError{index, err}
		}(index, update)
	}
	close(start)
	errorsByUpdate := make([]error, len(updates))
	for range updates {
		result := <-results
		errorsByUpdate[result.index] = result.err
	}
	return errorsByUpdate
}
