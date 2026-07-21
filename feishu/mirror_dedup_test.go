package feishu

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestHandleMessageEventDropsGroupMirrorAfterThreadFirst(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = 20 * time.Millisecond
	thread := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_thread", EventID: "evt_thread", ThreadID: "omt_thread",
		CreateTime: "1719730000000", Text: "@_user_1 D 线程的水果是什么？",
	})
	mirror := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_mirror", EventID: "evt_mirror",
		CreateTime: "1719730000000", Text: "@_user_1 D 线程的水果是什么？",
	})

	recorder := newDispatchRecorder()
	_ = adapter.handleMessageEvent(context.Background(), thread, recorder.dispatch)
	_ = adapter.handleMessageEvent(context.Background(), mirror, recorder.dispatch)

	recorder.requireCount(t, 1, 80*time.Millisecond)
	recorder.requireSessionEquals(t, "feishu:cli_a:tenant_1:group:oc_1")
}

func TestHandleMessageEventDropsPendingGroupMirrorWhenThreadArrives(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = 50 * time.Millisecond
	mirror := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_mirror", EventID: "evt_mirror",
		CreateTime: "1719730000000", Text: "@_user_1 D 线程的水果是什么？",
	})
	thread := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_thread", EventID: "evt_thread", ThreadID: "omt_thread",
		CreateTime: "1719730000000", Text: "@_user_1 D 线程的水果是什么？",
	})

	recorder := newDispatchRecorder()
	_ = adapter.handleMessageEvent(context.Background(), mirror, recorder.dispatch)
	_ = adapter.handleMessageEvent(context.Background(), thread, recorder.dispatch)

	recorder.requireCount(t, 1, 120*time.Millisecond)
	recorder.requireSessionEquals(t, "feishu:cli_a:tenant_1:group:oc_1")
}

func TestCanceledGroupMirrorReleasesReservationWhenPersistenceFails(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = 50 * time.Millisecond
	// 目录不能作为状态文件，确保取消镜像时的去重提交失败。
	adapter.SetDedupStateFile(t.TempDir())
	mirror := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_mirror_retry", EventID: "evt_mirror_retry",
		CreateTime: "1719730000000", Text: "@_user_1 D 线程的水果是什么？",
	})
	thread := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_thread_retry", EventID: "evt_thread_retry", ThreadID: "omt_thread_retry",
		CreateTime: "1719730000000", Text: "@_user_1 D 线程的水果是什么？",
	})

	if err := adapter.handleMessageEvent(context.Background(), mirror, newDispatchRecorder().dispatch); err != nil {
		t.Fatalf("defer mirror: %v", err)
	}
	if err := adapter.handleMessageEvent(context.Background(), thread, newDispatchRecorder().dispatch); err == nil {
		t.Fatal("thread admission should expose the configured persistence failure")
	}
	adapter.SetDedupStateFile(filepath.Join(t.TempDir(), "dedup.json"))
	reservation, duplicate := adapter.deduper.reserve(mirror, ExtractFeishuSessionScope(mirror))
	if duplicate {
		t.Fatal("failed mirror persistence must release its reservation for retry")
	}
	reservation.release()
}

func TestHandleMessageEventDispatchesPendingGroupWithoutThreadMirror(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = 20 * time.Millisecond
	event := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_group", EventID: "evt_group",
		CreateTime: "1719730000000", Text: "@_user_1 普通群聊问题",
	})

	recorder := newDispatchRecorder()
	_ = adapter.handleMessageEvent(context.Background(), event, recorder.dispatch)

	recorder.requireCount(t, 1, 80*time.Millisecond)
	recorder.requireSessionEquals(t, "feishu:cli_a:tenant_1:group:oc_1")
}

func TestHandleMessageEventDispatchesRepeatedPlainGroupMessagesWithoutThreadMirror(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = 20 * time.Millisecond
	first := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_group_a", EventID: "evt_group_a",
		CreateTime: "1719730000000", Text: "@_user_1 普通群聊问题",
	})
	second := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_group_b", EventID: "evt_group_b",
		CreateTime: "1719730000000", Text: "@_user_1 普通群聊问题",
	})

	recorder := newDispatchRecorder()
	_ = adapter.handleMessageEvent(context.Background(), first, recorder.dispatch)
	_ = adapter.handleMessageEvent(context.Background(), second, recorder.dispatch)

	recorder.requireCount(t, 2, 80*time.Millisecond)
}

func TestHandleMessageEventDispatchesSameContentInDifferentThreadsWithMirrorDedup(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = 20 * time.Millisecond
	first := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_a", EventID: "evt_a", ThreadID: "omt_thread_a",
		CreateTime: "1719730000000", Text: "@_user_1 同一个问题",
	})
	second := newDispatchableGroupEvent(dedupTestEventOptions{
		MessageID: "om_b", EventID: "evt_b", ThreadID: "omt_thread_b",
		CreateTime: "1719730000000", Text: "@_user_1 同一个问题",
	})

	dispatches := dispatchFeishuEvents(adapter, first, second)

	if dispatches != 2 {
		t.Fatalf("dispatches=%d, want same content in different threads dispatched twice", dispatches)
	}
}

func TestHandleMessageEventMirrorDedupDoesNotDelayDM(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.deduper.mirrorWindow = time.Second
	event := newDMEvent(dedupTestEventOptions{
		MessageID: "om_dm", EventID: "evt_dm",
		CreateTime: "1719730000000", Text: "hello",
	})

	recorder := newDispatchRecorder()
	_ = adapter.handleMessageEvent(context.Background(), event, recorder.dispatch)

	recorder.requireCount(t, 1, 10*time.Millisecond)
}

type dispatchRecorder struct {
	ch   chan platform.IncomingMessage
	seen []platform.IncomingMessage
}

func newDispatchRecorder() *dispatchRecorder {
	return &dispatchRecorder{ch: make(chan platform.IncomingMessage, 8)}
}

func (r *dispatchRecorder) dispatch(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
	r.ch <- msg
}

func (r *dispatchRecorder) requireCount(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for len(r.seen) < want {
		select {
		case msg := <-r.ch:
			r.seen = append(r.seen, msg)
		case <-deadline:
			t.Fatalf("dispatches=%d, want %d", len(r.seen), want)
		}
	}
	select {
	case msg := <-r.ch:
		r.seen = append(r.seen, msg)
		t.Fatalf("dispatches=%d, want %d", len(r.seen), want)
	case <-time.After(timeout):
	}
}

func (r *dispatchRecorder) requireSessionEquals(t *testing.T, value string) {
	t.Helper()
	if len(r.seen) == 0 {
		t.Fatal("no dispatched message")
	}
	session := r.seen[0].Metadata[feishuSessionMetadataKey]
	if session != value {
		t.Fatalf("session=%q, want %q", session, value)
	}
}
