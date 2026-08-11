package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexRemoteSelectionPersistsFrontendBindingOnly(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(filePath)
	workspace := "/workspace/a"
	bindingKey := codexBindingKey("route-a", "codex")
	store.ensureWorkspace(bindingKey, workspace)
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-shared")

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace,
		TargetThreadID: "thread-shared", ConversationID: "conversation-a",
		Expected: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if threadID, pending := store.getThread(bindingKey, workspace); pending || threadID != "thread-shared" {
		t.Fatalf("binding thread=%q pending=%v", threadID, pending)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var state codexSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != codexSessionStateVersion || len(state.Controls) != 0 {
		t.Fatalf("persisted state version=%d controls=%#v", state.Version, state.Controls)
	}
}

func TestCodexRemoteSelectionAllowsMultipleFrontendsOnSameThread(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/shared"
	keys := []string{codexBindingKey("route-a", "codex"), codexBindingKey("route-b", "codex")}
	updates := make([]codexRemoteSelectionUpdate, 0, len(keys))
	for index, key := range keys {
		store.ensureWorkspace(key, workspace)
		updates = append(updates, codexRemoteSelectionUpdate{
			BindingKey: key, WorkspaceRoot: workspace,
			TargetThreadID: "thread-shared", ConversationID: "conversation-" + string(rune('a'+index)),
			Expected: store.remoteSelectionSnapshot(key, "thread-shared"),
		})
	}

	start := make(chan struct{})
	errs := make(chan error, len(updates))
	var wg sync.WaitGroup
	for _, update := range updates {
		update := update
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.commitRemoteSelection(update)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("multi-frontend selection error=%v", err)
		}
	}
	for _, key := range keys {
		threadID, pending := store.getThread(key, workspace)
		if pending || threadID != "thread-shared" {
			t.Fatalf("binding %q thread=%q pending=%v", key, threadID, pending)
		}
	}
}

func TestCodexRemoteSelectionClearsExplicitReleaseTombstone(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/rebind"
	bindingKey := codexBindingKey("route-a", "codex")
	store.setActiveWorkspace(bindingKey, workspace)
	store.setThread(bindingKey, workspace, "thread-shared")
	if _, err := store.releaseWorkspaceThread(bindingKey, workspace); err != nil {
		t.Fatal(err)
	}
	if !store.workspaceReleased(bindingKey, workspace) {
		t.Fatal("release tombstone was not recorded")
	}

	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-shared")
	if _, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace,
		TargetThreadID: "thread-shared", ConversationID: "conversation-a",
		Expected: snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	if store.workspaceReleased(bindingKey, workspace) {
		t.Fatal("explicit re-selection must clear the release tombstone")
	}
	if threadID, pending := store.getThread(bindingKey, workspace); pending || threadID != "thread-shared" {
		t.Fatalf("rebound thread=%q pending=%v, want thread-shared false", threadID, pending)
	}
}

func TestCodexRemoteSelectionRejectsStaleSameFrontendBinding(t *testing.T) {
	store := newCodexSessionStore()
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-old")
	snapshot := store.remoteSelectionSnapshot(key, "thread-target")
	store.setThread(key, workspace, "thread-newer")

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-target", ConversationID: "conversation-a",
		Expected: snapshot,
	})
	if !errors.Is(err, errCodexRemoteSelectionChanged) {
		t.Fatalf("error=%v, want stale binding", err)
	}
}

func TestCodexRemoteSelectionWriteFailureKeepsLiveBinding(t *testing.T) {
	store := newCodexSessionStore()
	store.filePath = filepath.Join(t.TempDir(), "state.json")
	store.writeState = func(string, []byte) error { return errors.New("disk full") }
	key := codexBindingKey("route-a", "codex")
	workspace := "/workspace/a"
	store.setThread(key, workspace, "thread-old")
	snapshot := store.remoteSelectionSnapshot(key, "thread-target")

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace,
		TargetThreadID: "thread-target", ConversationID: "conversation-a",
		Expected: snapshot,
	})
	if err == nil {
		t.Fatal("commit error=nil")
	}
	if threadID, _ := store.getThread(key, workspace); threadID != "thread-old" {
		t.Fatalf("live binding=%q, want thread-old", threadID)
	}
}

func TestCodexAgentThreadRecordCannotReviveConcurrentRelease(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(filePath)
	bindingKey := codexBindingKey("route-a", "codex")
	workspace := "/workspace/release-race"
	store.setActiveWorkspace(bindingKey, workspace)
	store.setThread(bindingKey, workspace, "thread-old")

	releasePersisting := make(chan struct{})
	allowReleasePersist := make(chan struct{})
	var once sync.Once
	store.writeState = func(path string, data []byte) error {
		if bytes.Contains(data, []byte(`"ReleasePending": true`)) {
			once.Do(func() { close(releasePersisting) })
			<-allowReleasePersist
		}
		return writeCodexSessionStateFile(path, data)
	}
	releaseDone := make(chan error, 1)
	go func() {
		_, err := store.releaseWorkspaceThread(bindingKey, workspace)
		releaseDone <- err
	}()
	select {
	case <-releasePersisting:
	case <-time.After(time.Second):
		t.Fatal("release did not reach persistence fence")
	}
	recorded := make(chan bool, 1)
	recordErr := make(chan error, 1)
	go func() {
		ok, err := store.recordThreadUnlessReleased(bindingKey, workspace, "thread-old")
		recorded <- ok
		recordErr <- err
	}()
	close(allowReleasePersist)
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
	if err := <-recordErr; err != nil {
		t.Fatal(err)
	}
	if <-recorded {
		t.Fatal("late Agent mapping revived an explicitly released workspace")
	}
	if !store.workspaceReleased(bindingKey, workspace) {
		t.Fatal("release tombstone was cleared by late Agent mapping")
	}
}

func TestCodexRemoteSelectionPersistsFollowerEndpointAndReleaseClearsIt(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(filePath)
	bindingKey := codexBindingKey("feishu:tenant:dm:chat:user", "codex")
	workspace := "/workspace/follower"
	store.ensureWorkspace(bindingKey, workspace)
	follower := &codexFrontendFollower{
		WorkspaceRoot: workspace, ThreadID: "thread-shared", ActorUserID: "ou_user",
		DeliveryRoute: platform.DeliveryRoute{
			Platform: platform.PlatformFeishu, AccountID: "bot-a",
			ChatID: "oc_chat", ReplyToID: "om_switch",
		},
	}
	selection := store.remoteSelectionSnapshot(bindingKey, "thread-shared")
	if _, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace,
		TargetThreadID: "thread-shared", ConversationID: "conversation-a",
		SetFollower: true, Follower: follower, Expected: selection,
	}); err != nil {
		t.Fatal(err)
	}
	snapshots := store.followerSnapshots()
	if len(snapshots) != 1 || snapshots[0].BindingKey != bindingKey ||
		snapshots[0].Target.ThreadID != "thread-shared" ||
		snapshots[0].Target.DeliveryRoute != follower.DeliveryRoute || snapshots[0].Revision == 0 {
		t.Fatalf("follower snapshots=%#v", snapshots)
	}
	revision := snapshots[0].Revision

	// 浏览其他工作空间只改变导航层，不得悄悄移动长期同步目标。
	store.setActiveWorkspace(bindingKey, "/workspace/browsing")
	if got := store.followerSnapshots(); len(got) != 1 || got[0].Target.WorkspaceRoot != workspace {
		t.Fatalf("browse moved follower=%#v", got)
	}

	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(filePath)
	if got := reloaded.followerSnapshots(); len(got) != 1 || got[0].Target.DeliveryRoute != follower.DeliveryRoute {
		t.Fatalf("reloaded follower=%#v", got)
	}
	if _, err := reloaded.releaseWorkspaceThread(bindingKey, workspace); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.followerSnapshots(); len(got) != 0 {
		t.Fatalf("released follower still active=%#v", got)
	}
	reloaded.mu.Lock()
	binding := reloaded.bindings[bindingKey]
	reloaded.mu.Unlock()
	if binding.FollowRevision <= revision {
		t.Fatalf("release revision=%d, want greater than %d", binding.FollowRevision, revision)
	}
	if released := binding.Workspaces[workspace]; !released.ReleasePending || released.Released ||
		released.ReleasedThreadID != "thread-shared" {
		t.Fatalf("release tombstone=%#v", released)
	}
}

func TestCodexRemoteSelectionRejectsSecondFeishuFollowerForSameThread(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/shared-follower"
	firstKey := codexBindingKey("feishu:route-a", "codex")
	secondKey := codexBindingKey("feishu:route-b", "codex")
	commit := func(bindingKey, actor, chat string) error {
		store.ensureWorkspace(bindingKey, workspace)
		_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
			BindingKey: bindingKey, WorkspaceRoot: workspace,
			TargetThreadID: "thread-shared", ConversationID: "conversation-" + actor,
			SetFollower: true,
			Follower: &codexFrontendFollower{
				WorkspaceRoot: workspace, ThreadID: "thread-shared", ActorUserID: actor,
				DeliveryRoute: platform.DeliveryRoute{
					Platform: platform.PlatformFeishu, AccountID: "bot-a", ChatID: chat,
				},
			},
			Expected: store.remoteSelectionSnapshot(bindingKey, "thread-shared"),
		})
		return err
	}
	if err := commit(firstKey, "user-a", "chat-a"); err != nil {
		t.Fatal(err)
	}
	if err := commit(secondKey, "user-b", "chat-b"); !errors.Is(err, errCodexFollowerAlreadyBound) {
		t.Fatalf("second follower error=%v, want already bound", err)
	}
	if got := store.followerSnapshots(); len(got) != 1 || got[0].BindingKey != firstKey {
		t.Fatalf("followers after conflict=%#v", got)
	}
}

func TestCodexFirstTurnReplacementMovesFollowerTarget(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/first-turn"
	bindingKey := codexBindingKey("feishu:route-a", "codex")
	store.ensureWorkspace(bindingKey, workspace)
	if _, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace,
		TargetThreadID: "thread-placeholder", ConversationID: "conversation-a",
		PendingFirstTurn: true, SetFollower: true,
		Follower: &codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-placeholder", ActorUserID: "user-a",
			DeliveryRoute: platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "bot-a", ChatID: "chat-a"},
		},
		Expected: store.remoteSelectionSnapshot(bindingKey, "thread-placeholder"),
	}); err != nil {
		t.Fatal(err)
	}
	before := store.followerSnapshots()[0]
	if err := store.replaceRemoteFirstTurnThread(
		bindingKey, workspace, "conversation-a", "thread-placeholder", "thread-materialized",
	); err != nil {
		t.Fatal(err)
	}
	after := store.followerSnapshots()
	if len(after) != 1 || after[0].Target.ThreadID != "thread-materialized" || after[0].Revision <= before.Revision {
		t.Fatalf("follower after first-turn replacement=%#v, before=%#v", after, before)
	}
}

func TestCodexRemoteThreadReleaseClearsFollowerConflict(t *testing.T) {
	store := newCodexSessionStore()
	workspace := "/workspace/archive-follower"
	firstKey := codexBindingKey("feishu:route-a", "codex")
	secondKey := codexBindingKey("feishu:route-b", "codex")
	commitFollower := func(bindingKey, actor, chat string) error {
		store.ensureWorkspace(bindingKey, workspace)
		_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
			BindingKey: bindingKey, WorkspaceRoot: workspace,
			TargetThreadID: "thread-shared", ConversationID: "conversation-" + actor,
			SetFollower: true,
			Follower: &codexFrontendFollower{
				WorkspaceRoot: workspace, ThreadID: "thread-shared", ActorUserID: actor,
				DeliveryRoute: platform.DeliveryRoute{
					Platform: platform.PlatformFeishu, AccountID: "bot-a", ChatID: chat,
				},
			},
			Expected: store.remoteSelectionSnapshot(bindingKey, "thread-shared"),
		})
		return err
	}
	if err := commitFollower(firstKey, "user-a", "chat-a"); err != nil {
		t.Fatal(err)
	}
	before := store.followerSnapshots()[0].Revision
	if _, err := store.releaseRemoteThread(firstKey, "thread-shared"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	firstBinding := store.bindings[firstKey]
	store.mu.Unlock()
	if firstBinding.Follower != nil || firstBinding.FollowRevision <= before {
		t.Fatalf("released binding follower=%#v revision=%d", firstBinding.Follower, firstBinding.FollowRevision)
	}
	if err := commitFollower(secondKey, "user-b", "chat-b"); err != nil {
		t.Fatalf("stale released follower still blocks a new frontend: %v", err)
	}
}
