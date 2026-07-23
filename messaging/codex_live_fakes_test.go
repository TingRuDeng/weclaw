package messaging

import (
	"context"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeCodexLiveAgent struct {
	*fakeCodexThreadAgent
	mu                    sync.Mutex
	binding               agent.CodexThreadBinding
	bindings              map[string]agent.CodexThreadBinding
	bindErr               error
	inspectErrors         map[string]error
	handoffErr            error
	handoffErrors         map[string]error
	handoffAfterErrors    map[string]error
	handoffHistory        []agent.CodexRuntimeRequest
	handoffReleases       map[string]<-chan struct{}
	handoffHooks          map[string]func()
	rejectCanceledContext bool
	runErr                error
	bindCalls             int
	handoffCalls          int
	runCalls              int
	lastRuntimeReq        agent.CodexRuntimeRequest
	lastTurnReq           agent.CodexTurnRequest
	watchResults          []fakeCodexWatchResult
	inspectEntered        chan struct{}
	inspectRelease        <-chan struct{}
	handoffEntered        chan struct{}
	handoffRelease        <-chan struct{}
	turnEntered           chan struct{}
	turnRelease           <-chan struct{}
	recordRuntimeContext  func(string, context.Context, agent.CodexRuntimeRequest)
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
		handoffAfterErrors:   make(map[string]error),
		handoffReleases:      make(map[string]<-chan struct{}),
		handoffHooks:         make(map[string]func()),
	}
}

func (f *fakeCodexLiveAgent) InspectCodexRuntime(ctx context.Context, req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	entered, release := f.inspectEntered, f.inspectRelease
	rejectCanceled := f.rejectCanceledContext
	recordContext := f.recordRuntimeContext
	f.mu.Unlock()
	if recordContext != nil {
		recordContext("inspect", ctx, req)
	}
	if rejectCanceled && ctx.Err() != nil {
		return agent.CodexThreadBinding{}, ctx.Err()
	}
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

func (f *fakeCodexLiveAgent) CurrentCodexRuntime(req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	binding, ok := f.bindings[req.Ref.ThreadID]
	if !ok {
		binding = f.binding
	}
	binding.Ref = req.Ref
	binding.Control = req.Intent
	return binding, nil
}

func (f *fakeCodexLiveAgent) ReconcileCodexObservedTurn(_ context.Context, req agent.CodexRuntimeRequest, state agent.CodexThreadState) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	binding, ok := f.bindings[req.Ref.ThreadID]
	if !ok {
		binding = f.binding
	}
	if binding.Runtime == agent.CodexRuntimeConflict {
		return binding, agent.ErrCodexRuntimeConflict
	}
	intentEstablished := binding.Control.Owner != "" || binding.Control.RouteKey != "" ||
		binding.Control.ConversationID != "" || binding.Control.Revision != 0
	if intentEstablished && binding.Control != req.Intent {
		return binding, agent.ErrCodexControlChanged
	}
	state.ThreadID = req.Ref.ThreadID
	binding.Ref = req.Ref
	binding.Control = req.Intent
	binding.State = state
	if ok {
		f.bindings[req.Ref.ThreadID] = binding
	} else {
		f.binding = binding
	}
	return binding, nil
}

func (f *fakeCodexLiveAgent) HandoffCodexRuntime(ctx context.Context, req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	f.mu.Lock()
	f.handoffCalls++
	f.lastRuntimeReq = req
	f.handoffHistory = append(f.handoffHistory, req)
	entered, release := f.handoffEntered, f.handoffRelease
	if threadRelease, ok := f.handoffReleases[req.Ref.ThreadID]; ok {
		release = threadRelease
	}
	hook := f.handoffHooks[req.Ref.ThreadID]
	rejectCanceled := f.rejectCanceledContext
	recordContext := f.recordRuntimeContext
	handoffErr := f.handoffErr
	if err, ok := f.handoffErrors[req.Ref.ThreadID]; ok {
		handoffErr = err
	}
	f.mu.Unlock()
	if recordContext != nil {
		recordContext("handoff", ctx, req)
	}
	if hook != nil {
		hook()
	}
	if rejectCanceled && ctx.Err() != nil {
		return agent.CodexThreadBinding{}, ctx.Err()
	}
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
	if binding.Runtime == agent.CodexRuntimeConflict {
		binding.Runtime = agent.CodexRuntimeWeClaw
		binding.ConflictReason = ""
	}
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
	if err := f.handoffAfterErrors[req.Ref.ThreadID]; err != nil {
		return binding, err
	}
	return binding, nil
}

// MarkCodexRuntimeConflict 模拟 ACP registry 的持续 fail-closed 标记。
func (f *fakeCodexLiveAgent) MarkCodexRuntimeConflict(ctx context.Context, req agent.CodexRuntimeRequest) error {
	f.mu.Lock()
	recordContext := f.recordRuntimeContext
	f.mu.Unlock()
	if recordContext != nil {
		recordContext("mark", ctx, req)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	binding, ok := f.bindings[req.Ref.ThreadID]
	if !ok {
		binding = f.binding
	}
	intentEstablished := binding.Control.Owner != "" || binding.Control.RouteKey != "" ||
		binding.Control.ConversationID != "" || binding.Control.Revision != 0
	if binding.Control.Revision > req.Intent.Revision ||
		(binding.Control.Revision == req.Intent.Revision && intentEstablished &&
			(binding.Control.Owner != req.Intent.Owner || binding.Control.RouteKey != req.Intent.RouteKey ||
				binding.Control.ConversationID != req.Intent.ConversationID)) {
		return agent.ErrCodexControlChanged
	}
	binding.Ref = req.Ref
	binding.Control = req.Intent
	binding.State.ThreadID = req.Ref.ThreadID
	binding.Runtime = agent.CodexRuntimeConflict
	binding.ConflictReason = "控制权移交结果未确认"
	if ok {
		f.bindings[req.Ref.ThreadID] = binding
	} else {
		f.binding = binding
	}
	return nil
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
	if req.OnTurnStarted != nil {
		if err := req.OnTurnStarted(req.Runtime.Ref, "turn-fake"); err != nil {
			return "", err
		}
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

func (f *fakeCodexLiveAgent) runCallSnapshot() (int, agent.CodexTurnRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runCalls, f.lastTurnReq
}

func (f *fakeCodexLiveAgent) threadBinding(threadID string) agent.CodexThreadBinding {
	f.mu.Lock()
	defer f.mu.Unlock()
	if binding, ok := f.bindings[threadID]; ok {
		return binding
	}
	return f.binding
}

func codexLiveSwitchFixture(t *testing.T, state agent.CodexThreadState) (*Handler, *fakeCodexLiveAgent, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-1")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	ag.setThreadBinding("thread-1", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeWeClaw,
		State:   agent.CodexThreadState{ThreadID: "thread-1", Active: state.Active, ActiveTurnID: state.ActiveTurnID},
	})
	return h, ag, workspace
}

// setBindingState 同步测试探针与 thread 读取结果，模拟 in-process turn 的实时状态变化。
func (f *fakeCodexLiveAgent) setBindingState(state agent.CodexThreadState) {
	f.mu.Lock()
	f.binding.State = state
	f.fakeCodexThreadAgent.threadState = state
	f.mu.Unlock()
}
