package messaging

import (
	"context"
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
}

func newFakeCodexSessionCreateAgent(runtime agent.CodexRuntimeHolder, state agent.CodexThreadState) *fakeCodexSessionCreateAgent {
	return &fakeCodexSessionCreateAgent{fakeCodexLiveAgent: newFakeCodexLiveAgent(runtime, state)}
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
