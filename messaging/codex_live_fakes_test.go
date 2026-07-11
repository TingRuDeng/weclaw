package messaging

import (
	"context"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeCodexLiveAgent struct {
	*fakeCodexThreadAgent
	binding      agent.CodexThreadBinding
	bindErr      error
	bindCalls    int
	recoverCalls int
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
	f.bindCalls++
	f.binding.Ref = ref
	return f.binding, f.bindErr
}

func (f *fakeCodexLiveAgent) CurrentCodexThreadBinding(conversationID string) (agent.CodexThreadBinding, bool) {
	if f.binding.Ref.ConversationID != conversationID {
		return agent.CodexThreadBinding{}, false
	}
	return f.binding, true
}

func (f *fakeCodexLiveAgent) RecoverCodexThread(_ context.Context, ref agent.CodexThreadRef) error {
	f.recoverCalls++
	f.binding.Ref = ref
	f.binding.Owner = agent.CodexOwnerWeClawRuntime
	f.threadID = ref.ThreadID
	return nil
}
