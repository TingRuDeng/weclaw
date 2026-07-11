package messaging

import (
	"context"
	"sync"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeCodexLiveAgent struct {
	*fakeCodexThreadAgent
	mu           sync.Mutex
	binding      agent.CodexThreadBinding
	bindErr      error
	bindCalls    int
	recoverCalls int
	watchResults []fakeCodexWatchResult
}

type fakeCodexWatchResult struct {
	text string
	err  error
}

func newFakeCodexLiveAgent(owner agent.CodexRuntimeOwner, state agent.CodexThreadState) *fakeCodexLiveAgent {
	base := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "codex", Type: "acp", Command: "codex",
	}}, threadState: state}
	return &fakeCodexLiveAgent{
		fakeCodexThreadAgent: base,
		binding:              agent.CodexThreadBinding{Owner: owner, Connected: owner == agent.CodexOwnerDesktopLive, State: state},
	}
}

func (f *fakeCodexLiveAgent) BindCodexThread(_ context.Context, ref agent.CodexThreadRef) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindCalls++
	f.binding.Ref = ref
	return f.binding, f.bindErr
}

func (f *fakeCodexLiveAgent) CurrentCodexThreadBinding(conversationID string) (agent.CodexThreadBinding, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.binding.Ref.ConversationID != conversationID {
		return agent.CodexThreadBinding{}, false
	}
	return f.binding, true
}

func (f *fakeCodexLiveAgent) RecoverCodexThread(_ context.Context, ref agent.CodexThreadRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverCalls++
	f.binding.Ref = ref
	f.binding.Owner = agent.CodexOwnerWeClawRuntime
	f.threadID = ref.ThreadID
	return nil
}

func (f *fakeCodexLiveAgent) WatchCodexThread(ctx context.Context, conversationID string, threadID string, onProgress func(string)) (string, error) {
	f.mu.Lock()
	if len(f.watchResults) > 0 {
		result := f.watchResults[0]
		f.watchResults = f.watchResults[1:]
		f.mu.Unlock()
		return result.text, result.err
	}
	f.mu.Unlock()
	return f.fakeCodexThreadAgent.WatchCodexThread(ctx, conversationID, threadID, onProgress)
}

func (f *fakeCodexLiveAgent) setBindingOwner(owner agent.CodexRuntimeOwner) {
	f.mu.Lock()
	f.binding.Owner = owner
	f.binding.Connected = owner == agent.CodexOwnerDesktopLive
	f.mu.Unlock()
}
