package messaging

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestCodexControlIntentDefaultsToUnclaimed(t *testing.T) {
	store := newCodexSessionStore()

	intent := store.controlIntent("thread-1")

	if intent.Owner != codexControlUnclaimed || intent.Revision != 0 {
		t.Fatalf("intent=%#v，want unclaimed revision 0", intent)
	}
}

func TestCodexControlIntentPersistsRemoteRoute(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(stateFile)

	updated, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlRemote,
		RouteBindingKey: "route-1\x00codex", ConversationID: "conversation-1",
	})
	if err != nil {
		t.Fatalf("updateControlIntent() error=%v", err)
	}
	if updated.Revision != 1 {
		t.Fatalf("revision=%d，want 1", updated.Revision)
	}

	restored := newCodexSessionStore()
	restored.SetFilePath(stateFile)
	intent := restored.controlIntent("thread-1")
	if intent.Owner != codexControlRemote || intent.RouteBindingKey != "route-1\x00codex" ||
		intent.ConversationID != "conversation-1" || intent.Revision != 1 {
		t.Fatalf("restored intent=%#v", intent)
	}
}

func TestCodexControlIntentRejectsStaleRevision(t *testing.T) {
	store := newCodexSessionStore()
	first, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlRemote,
		RouteBindingKey: "route-1", ConversationID: "conversation-1",
		ExpectedRevision: first.Revision - 1,
	})
	if !errors.Is(err, errCodexControlRevisionChanged) {
		t.Fatalf("error=%v，want revision changed", err)
	}
	if got := store.controlIntent("thread-1"); got.Owner != codexControlDesktop || got.Revision != first.Revision {
		t.Fatalf("intent=%#v，want unchanged desktop", got)
	}
}

func TestCodexControlIntentRequiresRemoteRoute(t *testing.T) {
	store := newCodexSessionStore()

	_, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlRemote,
	})

	if !errors.Is(err, errCodexControlRouteRequired) {
		t.Fatalf("error=%v，want route required", err)
	}
}

func TestCodexControlIntentRollsBackWhenPersistenceFails(t *testing.T) {
	store := newCodexSessionStore()
	store.SetFilePath(t.TempDir())

	_, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlDesktop,
	})

	if err == nil {
		t.Fatal("状态文件替换失败时不应提交控制意图")
	}
	intent := store.controlIntent("thread-1")
	if intent.Owner != codexControlUnclaimed || intent.Revision != 0 {
		t.Fatalf("intent=%#v，want rolled back unclaimed", intent)
	}
}

func TestCodexControlIntentConcurrentClaimHasSingleWinner(t *testing.T) {
	store := newCodexSessionStore()
	updates := []codexControlIntentUpdate{
		{ThreadID: "thread-1", Owner: codexControlRemote, RouteBindingKey: "route-a", ConversationID: "conversation-a"},
		{ThreadID: "thread-1", Owner: codexControlRemote, RouteBindingKey: "route-b", ConversationID: "conversation-b"},
	}
	start := make(chan struct{})
	results := make(chan error, len(updates))
	var wg sync.WaitGroup
	for _, update := range updates {
		update := update
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.updateControlIntent(update)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, errCodexControlRevisionChanged) {
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d，want 1", successes)
	}
}
