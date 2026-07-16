package messaging

import (
	"context"
	"errors"
	"sync"

	"github.com/fastclaw-ai/weclaw/agent"
)

// fakeCodexSessionCreateAgent 模拟 ResetSession 会先改变 ACP mapping 的创建语义。
type fakeCodexSessionCreateAgent struct {
	*fakeCodexLiveAgent
	resetMu           sync.Mutex
	resetSessionID    string
	resetErr          error
	resetCalls        int
	resetConversation string
	resetEntered      chan struct{}
	resetRelease      <-chan struct{}
	rejectCanceledUse bool
	useContextErrors  []error
	conflictMarks     []string
	markErrors        map[string]error
}

func newFakeCodexSessionCreateAgent(runtime agent.CodexRuntimeHolder, state agent.CodexThreadState) *fakeCodexSessionCreateAgent {
	return &fakeCodexSessionCreateAgent{
		fakeCodexLiveAgent: newFakeCodexLiveAgent(runtime, state),
		markErrors:         make(map[string]error),
	}
}

func (f *fakeCodexSessionCreateAgent) ResetSession(ctx context.Context, conversationID string) (string, error) {
	f.resetMu.Lock()
	f.resetCalls++
	f.resetConversation = conversationID
	created, resetErr := f.resetSessionID, f.resetErr
	entered, release := f.resetEntered, f.resetRelease
	f.resetMu.Unlock()
	signalCodexLiveTestHook(entered)
	if err := waitCodexLiveTestHook(ctx, release); err != nil {
		return "", err
	}
	// 真实 ACP 在创建失败或成功时都可能已改变旧 conversation mapping。
	f.fakeCodexThreadAgent.threadID = created
	return created, resetErr
}

func (f *fakeCodexSessionCreateAgent) resetSnapshot() (int, string) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	return f.resetCalls, f.resetConversation
}

func (f *fakeCodexSessionCreateAgent) UseCodexThread(ctx context.Context, conversationID string, threadID string) error {
	f.resetMu.Lock()
	f.useContextErrors = append(f.useContextErrors, ctx.Err())
	rejectCanceled := f.rejectCanceledUse
	f.resetMu.Unlock()
	if rejectCanceled && ctx.Err() != nil {
		return ctx.Err()
	}
	return f.fakeCodexThreadAgent.UseCodexThread(ctx, conversationID, threadID)
}

func (f *fakeCodexSessionCreateAgent) MarkCodexRuntimeConflict(ctx context.Context, req agent.CodexRuntimeRequest) error {
	f.resetMu.Lock()
	f.conflictMarks = append(f.conflictMarks, req.Ref.ThreadID)
	markErr := f.markErrors[req.Ref.ThreadID]
	f.resetMu.Unlock()
	f.mu.Lock()
	if _, exists := f.bindings[req.Ref.ThreadID]; !exists {
		binding := f.binding
		binding.Ref = req.Ref
		binding.Control = req.Intent
		binding.State.ThreadID = req.Ref.ThreadID
		f.bindings[req.Ref.ThreadID] = binding
	}
	f.mu.Unlock()
	// 真实 registry 会把 conversation 最后指向最近标记的 thread。
	f.fakeCodexThreadAgent.threadID = req.Ref.ThreadID
	if err := f.fakeCodexLiveAgent.MarkCodexRuntimeConflict(ctx, req); err != nil {
		return errors.Join(markErr, err)
	}
	return markErr
}

func (f *fakeCodexSessionCreateAgent) conflictSnapshot() ([]string, []error) {
	f.resetMu.Lock()
	defer f.resetMu.Unlock()
	return append([]string(nil), f.conflictMarks...), append([]error(nil), f.useContextErrors...)
}

func (f *fakeCodexSessionCreateAgent) runCallSnapshot() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runCalls
}
