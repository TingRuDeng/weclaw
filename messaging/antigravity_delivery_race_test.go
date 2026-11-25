package messaging

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

type blockingOpenReplier struct {
	*antigravityFakeReplier
	started  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (r *blockingOpenReplier) OpenStream(ctx context.Context, _ platform.StreamOptions) (platform.Stream, error) {
	defer close(r.finished)
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingResultReplier struct {
	*antigravityFakeReplier
	started  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (r *blockingResultReplier) SendResultIdempotent(ctx context.Context, _ platform.TerminalResult, _ string) error {
	defer close(r.finished)
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestAntigravityClearWaitsForInFlightOpenStream(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	base := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	reply := &blockingOpenReplier{antigravityFakeReplier: base, started: make(chan struct{}), finished: make(chan struct{})}
	client := &antigravityFakeClient{conversations: []antigravity.Conversation{{ID: "conv-a", Title: "Alpha"}}}
	client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
		return antigravity.ObserveResult{
			Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
			Events:       []antigravity.Event{{Kind: antigravity.EventProgress, Text: "progress"}},
			NextFileID:   "f1", NextOffset: offset + 1,
		}, nil
	}
	h := newAntigravityTestHandler(client)
	observer := h.installAntigravityBinding(msg, route, reply, client.conversations[0], antigravity.Baseline{})
	select {
	case <-reply.started:
	case <-time.After(3 * time.Second):
		t.Fatal("OpenStream was not reached")
	}
	cleared := make(chan struct{})
	go func() {
		h.clearAntigravityBinding(msg.AccountID, route)
		close(cleared)
	}()
	select {
	case <-cleared:
	case <-time.After(3 * time.Second):
		t.Fatal("clear did not return after cancelling the in-flight write")
	}
	select {
	case <-reply.finished:
	case <-time.After(time.Second):
		t.Fatal("in-flight OpenStream was not cancelled before clear returned")
	}
	observer.updateStream("late progress")
	observer.deliverTurn("late answer", 99)
	time.Sleep(50 * time.Millisecond)
	if results := base.snapshotResults(); len(results) != 0 {
		t.Fatalf("old observer delivered after clear: %+v", results)
	}
	if texts := base.snapshotTexts(); len(texts) != 0 {
		t.Fatalf("old observer sent text after clear: %+v", texts)
	}
}

func TestAntigravitySwitchWaitsForInFlightOpenStream(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	oldBase := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	oldReply := &blockingOpenReplier{antigravityFakeReplier: oldBase, started: make(chan struct{}), finished: make(chan struct{})}
	client := &antigravityFakeClient{conversations: []antigravity.Conversation{
		{ID: "conv-a", Title: "Alpha", ProjectName: "Project"},
		{ID: "conv-b", Title: "Beta", ProjectName: "Project"},
	}}
	client.observeFn = func(conversationID, fileID string, offset int64) (antigravity.ObserveResult, error) {
		return antigravity.ObserveResult{
			Conversation: antigravity.Conversation{ID: conversationID, RunState: "RUN_STATE_RUNNING"},
			Events:       []antigravity.Event{{Kind: antigravity.EventProgress, Text: "progress"}},
			NextFileID:   "f1", NextOffset: offset + 1,
		}, nil
	}
	h := newAntigravityTestHandler(client)
	oldObserver := h.installAntigravityBinding(msg, route, oldReply, client.conversations[0], antigravity.Baseline{})
	select {
	case <-oldReply.started:
	case <-time.After(3 * time.Second):
		t.Fatal("OpenStream was not reached")
	}
	client.observeFn = nil
	newReply := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	switched := make(chan struct{})
	go func() {
		h.installAntigravityBinding(msg, route, newReply, client.conversations[1], antigravity.Baseline{})
		close(switched)
	}()
	select {
	case <-switched:
	case <-time.After(3 * time.Second):
		t.Fatal("switch did not return after cancelling the in-flight write")
	}
	select {
	case <-oldReply.finished:
	case <-time.After(time.Second):
		t.Fatal("in-flight OpenStream was not cancelled before switch returned")
	}
	oldObserver.deliverTurn("late answer", 99)
	time.Sleep(50 * time.Millisecond)
	if results := oldBase.snapshotResults(); len(results) != 0 {
		t.Fatalf("old observer delivered after switch: %+v", results)
	}
}

func TestAntigravityRevocationBarrierStopsInFlightResult(t *testing.T) {
	route := "feishu:bot:tenant:dm:chat:user"
	msg := antigravityTestMessage(route)
	msg.UserAliases = []string{"union_1"}
	base := newAntigravityFakeReplier(platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: msg.AccountID, ChatID: msg.ChatID})
	reply := &blockingResultReplier{antigravityFakeReplier: base, started: make(chan struct{}), finished: make(chan struct{})}
	fakePlatform := &antigravityFakePlatform{
		name: platform.PlatformFeishu, account: msg.AccountID, reply: reply,
		access: platform.NewAccessControl([]string{"union_1"}),
	}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: fakePlatform, Access: platform.NewAccessControl([]string{"union_1"}),
	}})
	authorized, ok := registry.AuthorizeIncomingMessage(msg)
	if !ok {
		t.Fatal("message must authorize")
	}
	client := &antigravityFakeClient{}
	h := newAntigravityTestHandler(client)
	h.SetPlatformRegistry(registry)
	observer := h.installAntigravityBinding(authorized, route, reply, antigravity.Conversation{ID: "conv-a", Title: "Alpha"}, antigravity.Baseline{})
	delivered := make(chan struct{})
	go func() {
		observer.deliverTurn("in-flight answer", 0)
		close(delivered)
	}()
	select {
	case <-reply.started:
	case <-time.After(3 * time.Second):
		t.Fatal("SendResultIdempotent was not reached")
	}
	registry.UpdateAccessForAccount(platform.PlatformFeishu, msg.AccountID, []string{"someone-else"})
	cleared := make(chan struct{})
	go func() {
		h.clearAntigravityBinding(msg.AccountID, route)
		close(cleared)
	}()
	select {
	case <-cleared:
	case <-time.After(3 * time.Second):
		t.Fatal("clear did not return after revocation")
	}
	select {
	case <-reply.finished:
	case <-time.After(time.Second):
		t.Fatal("in-flight result was not cancelled before clear returned")
	}
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight delivery did not observe cancellation")
	}
	if results := base.snapshotResults(); len(results) != 0 {
		t.Fatalf("revoked delivery was recorded: %+v", results)
	}
	observer.deliverTurn("late answer", 10)
	time.Sleep(50 * time.Millisecond)
	if results := base.snapshotResults(); len(results) != 0 {
		t.Fatalf("old observer delivered after revocation: %+v", results)
	}
}
