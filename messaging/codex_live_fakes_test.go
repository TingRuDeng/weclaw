package messaging

import (
	"context"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeCodexLiveAgent struct {
	*fakeCodexThreadAgent
	mu             sync.Mutex
	binding        agent.CodexThreadBinding
	bindings       map[string]agent.CodexThreadBinding
	bindErr        error
	inspectErrors  map[string]error
	handoffErr     error
	handoffErrors  map[string]error
	handoffHistory []agent.CodexRuntimeRequest
	runErr         error
	bindCalls      int
	handoffCalls   int
	runCalls       int
	lastRuntimeReq agent.CodexRuntimeRequest
	lastTurnReq    agent.CodexTurnRequest
	watchResults   []fakeCodexWatchResult
	inspectEntered chan struct{}
	inspectRelease <-chan struct{}
	handoffEntered chan struct{}
	handoffRelease <-chan struct{}
	turnEntered    chan struct{}
	turnRelease    <-chan struct{}
}

type fakeCodexWatchResult struct {
	text string
	err  error
}

func newFakeCodexLiveAgent(runtime agent.CodexRuntimeHolder, state agent.CodexThreadState) *fakeCodexLiveAgent {
	base := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "codex", Type: "acp", Command: "codex",
	}}, threadState: state}
	return &fakeCodexLiveAgent{
		fakeCodexThreadAgent: base,
		binding:              agent.CodexThreadBinding{Runtime: runtime, State: state},
		bindings:             make(map[string]agent.CodexThreadBinding),
		inspectErrors:        make(map[string]error),
		handoffErrors:        make(map[string]error),
	}
}

func (f *fakeCodexLiveAgent) InspectCodexRuntime(ctx context.Context, req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	entered, release := f.inspectEntered, f.inspectRelease
	f.mu.Unlock()
	signalCodexLiveTestHook(entered)
	if err := waitCodexLiveTestHook(ctx, release); err != nil {
		return agent.CodexThreadBinding{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindCalls++
	f.lastRuntimeReq = req
	binding, ok := f.bindings[req.Ref.ThreadID]
	if !ok {
		binding = f.binding
	}
	binding.Ref = req.Ref
	binding.Control = req.Intent
	if ok {
		f.bindings[req.Ref.ThreadID] = binding
	} else {
		f.binding = binding
	}
	if err, exists := f.inspectErrors[req.Ref.ThreadID]; exists {
		return agent.CodexThreadBinding{}, err
	}
	return binding, f.bindErr
}

func (f *fakeCodexLiveAgent) HandoffCodexRuntime(ctx context.Context, req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	f.handoffCalls++
	f.lastRuntimeReq = req
	f.handoffHistory = append(f.handoffHistory, req)
	entered, release := f.handoffEntered, f.handoffRelease
	handoffErr := f.handoffErr
	if err, ok := f.handoffErrors[req.Ref.ThreadID]; ok {
		handoffErr = err
	}
	f.mu.Unlock()
	signalCodexLiveTestHook(entered)
	if err := waitCodexLiveTestHook(ctx, release); err != nil {
		return agent.CodexThreadBinding{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if handoffErr != nil {
		return agent.CodexThreadBinding{}, handoffErr
	}
	binding, ok := f.bindings[req.Ref.ThreadID]
	if !ok {
		binding = f.binding
	}
	binding.Ref = req.Ref
	binding.Control = req.Intent
	if req.Intent.Owner == agent.CodexControlRemote && binding.Runtime == agent.CodexRuntimeUnknown {
		binding.Runtime = agent.CodexRuntimeWeClaw
	}
	if req.Intent.Owner == agent.CodexControlDesktop && binding.Runtime == agent.CodexRuntimeWeClaw {
		binding.Runtime = agent.CodexRuntimeUnknown
	}
	if ok {
		f.bindings[req.Ref.ThreadID] = binding
	} else {
		f.binding = binding
	}
	return binding, nil
}

func (f *fakeCodexLiveAgent) RunCodexTurn(ctx context.Context, req agent.CodexTurnRequest) (string, error) {
	f.mu.Lock()
	f.runCalls++
	f.lastTurnReq = req
	runErr := f.runErr
	entered, release := f.turnEntered, f.turnRelease
	f.mu.Unlock()
	if runErr != nil {
		return "", runErr
	}
	signalCodexLiveTestHook(entered)
	if err := waitCodexLiveTestHook(ctx, release); err != nil {
		return "", err
	}
	return f.fakeCodexThreadAgent.Chat(ctx, req.Runtime.Ref.ConversationID, req.Message)
}

func signalCodexLiveTestHook(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func waitCodexLiveTestHook(ctx context.Context, release <-chan struct{}) error {
	if release == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-release:
		return nil
	}
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

func (f *fakeCodexLiveAgent) setBindingRuntime(runtime agent.CodexRuntimeHolder) {
	f.mu.Lock()
	f.binding.Runtime = runtime
	f.mu.Unlock()
}

func (f *fakeCodexLiveAgent) setThreadBinding(threadID string, binding agent.CodexThreadBinding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	binding.State.ThreadID = threadID
	f.bindings[threadID] = binding
}

func (f *fakeCodexLiveAgent) handoffRequests() []agent.CodexRuntimeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.CodexRuntimeRequest(nil), f.handoffHistory...)
}

// setBindingState 同步测试探针与 thread 读取结果，模拟 in-process turn 的实时状态变化。
func (f *fakeCodexLiveAgent) setBindingState(state agent.CodexThreadState) {
	f.mu.Lock()
	f.binding.State = state
	f.fakeCodexThreadAgent.threadState = state
	f.mu.Unlock()
}

type fakeRemoteControlOptions struct {
	routeUserID string
	agentName   string
	bindingKey  string
	workspace   string
	threadID    string
}

// claimRemoteControlForTest 为测试窗口建立显式远程控制意图。
func claimRemoteControlForTest(t *testing.T, h *Handler, opts fakeRemoteControlOptions) {
	t.Helper()
	conversationID := buildCodexConversationID(opts.routeUserID, opts.agentName, opts.workspace)
	_, err := h.ensureCodexSessions().updateControlIntent(codexControlIntentUpdate{
		ThreadID: opts.threadID, Owner: codexControlRemote,
		RouteBindingKey: opts.bindingKey, ConversationID: conversationID,
	})
	if err != nil {
		t.Fatalf("建立测试控制意图失败: %v", err)
	}
}
