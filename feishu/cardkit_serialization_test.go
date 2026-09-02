package feishu

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type cardKitUpdateCall struct {
	cardID   string
	sequence int
}

// blockingCardKitClient 让第一笔全量更新停在网络边界，验证同一张卡的
// 后续更新不会在更早的序号真正发出前越过它。
type blockingCardKitClient struct {
	fakeCardKitClient
	mu            sync.Mutex
	calls         []cardKitUpdateCall
	firstEntered  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
}

func (c *blockingCardKitClient) UpdateCard(ctx context.Context, cardID string, cardJSON string, sequence int) error {
	c.mu.Lock()
	c.calls = append(c.calls, cardKitUpdateCall{cardID: cardID, sequence: sequence})
	callNumber := len(c.calls)
	c.mu.Unlock()

	switch callNumber {
	case 1:
		c.firstOnce.Do(func() { close(c.firstEntered) })
		select {
		case <-c.releaseFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	case 2:
		c.secondOnce.Do(func() { close(c.secondEntered) })
	}
	return nil
}

func TestCardKitUpdatesForOneTaskCardAreSerializedBeforeNetwork(t *testing.T) {
	kit := &blockingCardKitClient{
		firstEntered:  make(chan struct{}),
		secondEntered: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	registry := newTaskCardRegistry()
	registry.recordWithSequence("card-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "旧内容",
	}, 1)
	stream := &feishuStream{
		cardKit:   kit,
		taskCards: registry,
		cardID:    "card-1",
		title:     "Codex",
		throttle:  0,
		now:       time.Now,
	}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, registry)
	choice := automaticApprovalChoiceForTest("approval-1", "card-1")
	prompt := approvalPromptForTest("date")

	streamErr := make(chan error, 1)
	go func() { streamErr <- stream.Update(context.Background(), "新内容") }()
	select {
	case <-kit.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first task-card update")
	}

	approvalErr := make(chan error, 1)
	go func() { approvalErr <- reply.RecordAutomaticApproval(context.Background(), prompt, choice) }()
	select {
	case <-kit.secondEntered:
		t.Fatal("a later task-card update reached CardKit before the first update completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(kit.releaseFirst)

	if err := <-streamErr; err != nil {
		t.Fatalf("stream update error: %v", err)
	}
	if err := <-approvalErr; err != nil {
		t.Fatalf("automatic approval error: %v", err)
	}

	kit.mu.Lock()
	calls := append([]cardKitUpdateCall(nil), kit.calls...)
	kit.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("CardKit calls=%#v, want exactly two serialized updates", calls)
	}
	if calls[0].sequence >= calls[1].sequence {
		t.Fatalf("CardKit sequences=%#v, want strictly increasing network order", calls)
	}
	if calls[0].cardID != "card-1" || calls[1].cardID != "card-1" {
		t.Fatalf("CardKit card IDs=%#v, want both updates on card-1", calls)
	}
}

func TestAutomaticApprovalRecoveryCallbackDoesNotDeadlockWithProgressUpdate(t *testing.T) {
	kit := &fakeCardKitClient{}
	registry := newTaskCardRegistry()
	registry.recordWithSequence("card-1", cardOptions{
		Status:  cardStatusThinking,
		Title:   "Codex",
		Content: "旧内容",
	}, 1)
	stream := &feishuStream{
		cardKit:   kit,
		taskCards: registry,
		cardID:    "card-1",
		title:     "Codex",
		throttle:  0,
		now:       time.Now,
	}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, registry)

	var progressMu sync.Mutex
	progressLocked := make(chan struct{})
	startProgressUpdate := make(chan struct{})
	progressDone := make(chan error, 1)
	go func() {
		progressMu.Lock()
		close(progressLocked)
		<-startProgressUpdate
		err := stream.Update(context.Background(), "新进展")
		progressMu.Unlock()
		progressDone <- err
	}()
	<-progressLocked

	callbackEntered := make(chan struct{})
	var callbackOnce sync.Once
	callbackCompletions := 0
	registry.setDurableReferenceChangeHandler("card-1", func() {
		callbackOnce.Do(func() { close(callbackEntered) })
		progressMu.Lock()
		callbackCompletions++
		progressMu.Unlock()
	})
	approvalDone := make(chan error, 1)
	go func() {
		approvalDone <- reply.RecordAutomaticApproval(
			context.Background(),
			approvalPromptForTest("date"),
			automaticApprovalChoiceForTest("approval-1", "card-1"),
		)
	}()

	select {
	case <-callbackEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for automatic approval recovery callback")
	}
	close(startProgressUpdate)

	deadline := time.After(time.Second)
	for progressDone != nil || approvalDone != nil {
		select {
		case err := <-progressDone:
			if err != nil {
				t.Fatalf("progress update error: %v", err)
			}
			progressDone = nil
		case err := <-approvalDone:
			if err != nil {
				t.Fatalf("automatic approval error: %v", err)
			}
			approvalDone = nil
		case <-deadline:
			t.Fatal("automatic approval and progress update deadlocked")
		}
	}
	progressMu.Lock()
	completed := callbackCompletions
	progressMu.Unlock()
	if completed != 1 {
		t.Fatalf("recovery callback completions=%d, want 1", completed)
	}
}

var _ cardKitClient = (*blockingCardKitClient)(nil)

var _ platform.Stream = (*feishuStream)(nil)
