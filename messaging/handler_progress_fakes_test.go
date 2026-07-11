package messaging

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeProgressAgent struct {
	fakeAgent
	progressCalled bool
	progressDeltas []string
	delay          time.Duration
}

func (f *fakeProgressAgent) ChatWithProgress(_ context.Context, _ string, _ string, onProgress func(delta string)) (string, error) {
	f.progressCalled = true
	for _, delta := range f.progressDeltas {
		if onProgress != nil {
			onProgress(delta)
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.reply, f.err
}

type blockingProgressAgent struct {
	fakeAgent
	mu        sync.Mutex
	started   int
	active    int
	maxActive int
	entered   chan struct{}
	release   chan struct{}
}

func newBlockingProgressAgent() *blockingProgressAgent {
	return &blockingProgressAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
}

func (f *blockingProgressAgent) ChatWithProgress(ctx context.Context, _ string, _ string, _ func(delta string)) (string, error) {
	callIndex := f.markStarted()
	f.entered <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		f.markFinished()
		return "", ctx.Err()
	}
	f.markFinished()
	return fmt.Sprintf("第%d条结果", callIndex), f.err
}

func (f *blockingProgressAgent) markStarted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	return f.started
}

func (f *blockingProgressAgent) markFinished() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
}

func (f *blockingProgressAgent) stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.maxActive
}

type blockingCodexThreadAgent struct {
	fakeCodexThreadAgent
	mu               sync.Mutex
	started          int
	active           int
	maxActive        int
	entered          chan struct{}
	release          chan struct{}
	threads          map[string]string
	conversationCwds map[string]string
}

func newBlockingCodexThreadAgent() *blockingCodexThreadAgent {
	return &blockingCodexThreadAgent{
		fakeCodexThreadAgent: fakeCodexThreadAgent{
			fakeAgent: fakeAgent{
				info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			},
		},
		entered:          make(chan struct{}, 4),
		release:          make(chan struct{}),
		threads:          make(map[string]string),
		conversationCwds: make(map[string]string),
	}
}

func (f *blockingCodexThreadAgent) ChatWithProgress(ctx context.Context, conversationID string, _ string, _ func(delta string)) (string, error) {
	callIndex := f.markStarted(conversationID)
	f.entered <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		f.markFinished()
		return "", ctx.Err()
	}
	f.markFinished()
	return fmt.Sprintf("第%d条结果", callIndex), nil
}

func (f *blockingCodexThreadAgent) CurrentCodexThread(conversationID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	threadID := f.threads[conversationID]
	return threadID, threadID != ""
}

func (f *blockingCodexThreadAgent) UseCodexThread(_ context.Context, conversationID string, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.useConversation = conversationID
	f.useThreadID = threadID
	if f.useErr != nil {
		return f.useErr
	}
	f.threads[conversationID] = threadID
	return nil
}

func (f *blockingCodexThreadAgent) ClearCodexThread(conversationID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalledWith = conversationID
	delete(f.threads, conversationID)
}

func (f *blockingCodexThreadAgent) SetConversationCwd(conversationID string, cwd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversationCwds[conversationID] = cwd
}

func (f *blockingCodexThreadAgent) markStarted(conversationID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.threads[conversationID] = fmt.Sprintf("thread-generated-%d", f.started)
	return f.started
}

func (f *blockingCodexThreadAgent) markFinished() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
}

func (f *blockingCodexThreadAgent) conversationCwd(conversationID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conversationCwds[conversationID]
}
