package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestCodexDesktopStateWaitsForRequestedRevision 验证 IPC 响应不能早于状态投影完成。
func TestCodexDesktopStateWaitsForRequestedRevision(t *testing.T) {
	store := newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	applyDesktopRefreshSnapshot(t, store, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.waitForRevision(ctx, "thread-1", 1, 2) }()
	select {
	case err := <-done:
		t.Fatalf("waitForRevision() 提前返回: %v", err)
	default:
	}
	applyDesktopRefreshSnapshot(t, store, 2)
	if err := <-done; err != nil {
		t.Fatalf("waitForRevision() error = %v", err)
	}
}

// TestCodexDesktopRuntimeIgnoresUntrackedThreads 验证全局旧广播不会进入接管状态缓存。
func TestCodexDesktopRuntimeIgnoresUntrackedThreads(t *testing.T) {
	runtime := newCodexDesktopRuntime()
	runtime.client = &codexDesktopClient{epoch: 1}
	runtime.state = newCodexDesktopStateStore(codexDesktopStateOptions{now: time.Now})
	runtime.handleBroadcast(desktopRefreshEnvelope(t, 1))
	if runtime.state.threadCount() != 0 {
		t.Fatalf("untracked thread count = %d", runtime.state.threadCount())
	}
	runtime.trackThread("thread-1")
	runtime.handleBroadcast(desktopRefreshEnvelope(t, 2))
	if runtime.state.threadCount() != 1 {
		t.Fatalf("tracked thread count = %d", runtime.state.threadCount())
	}
}

// TestCodexDesktopLoadRevisionRequiresPositiveRevision 验证 history 响应必须提供状态屏障。
func TestCodexDesktopLoadRevisionRequiresPositiveRevision(t *testing.T) {
	revision, err := codexDesktopLoadRevision(json.RawMessage(`{"revision":42}`))
	if err != nil || revision != 42 {
		t.Fatalf("codexDesktopLoadRevision() = %d, %v", revision, err)
	}
	if _, err := codexDesktopLoadRevision(json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing revision should fail")
	}
}

// applyDesktopRefreshSnapshot 写入指定 revision 的最小状态。
func applyDesktopRefreshSnapshot(t *testing.T, store *codexDesktopStateStore, revision uint64) {
	t.Helper()
	_, err := store.applySnapshot(codexDesktopSnapshotSpec{
		threadID: "thread-1", epoch: 1, revision: revision,
		raw: desktopStateFixture("thread-1", "idle"),
	})
	if err != nil {
		t.Fatalf("applySnapshot() error = %v", err)
	}
}

// desktopRefreshEnvelope 构造可由 runtime 处理的 snapshot 广播。
func desktopRefreshEnvelope(t *testing.T, revision uint64) codexDesktopEnvelope {
	t.Helper()
	params, err := json.Marshal(codexDesktopStateParams{
		ConversationID: "thread-1",
		Change: codexDesktopStateChange{
			Type: "snapshot", Revision: revision,
			ConversationState: desktopStateFixture("thread-1", "idle"),
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return codexDesktopEnvelope{
		Type: codexDesktopEnvelopeBroadcast, Method: "thread-stream-state-changed",
		Version: 11, Params: params,
	}
}
