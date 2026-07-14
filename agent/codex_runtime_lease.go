package agent

import (
	"fmt"
	"strings"
	"sync"
)

type codexWriterLeaseState struct {
	runtimeGeneration    uint64
	controlRevision      uint64
	runtime              CodexRuntimeHolder
	routeKey             string
	turnID               string
	candidateDesktopTurn string
	baselineLastTurnID   string
	conflict             bool
	conflictCh           chan struct{}
	conflictOnce         sync.Once
}

type codexWriterLease struct {
	registry *codexRuntimeOwnerRegistry
	threadID string
	state    *codexWriterLeaseState
}

type codexBindingActivation struct {
	request CodexRuntimeRequest
	runtime CodexRuntimeHolder
	state   CodexThreadState
}

// activateRuntime 记录已验证的用户控制意图与当前可用 writer。
func (r *codexRuntimeOwnerRegistry) activateRuntime(req CodexRuntimeRequest, runtime CodexRuntimeHolder, state CodexThreadState) (CodexThreadBinding, error) {
	if err := validateCodexRuntimeRequest(req); err != nil {
		return CodexThreadBinding{}, err
	}
	if !validCodexRuntimeHolder(runtime) {
		return CodexThreadBinding{}, fmt.Errorf("无效的 Codex runtime %q", runtime)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	threadID := req.Ref.ThreadID
	if r.leases[threadID] != nil {
		return r.bindingDuringWriterLease(req, runtime)
	}
	binding := r.threads[threadID]
	binding = activateCodexBinding(binding, codexBindingActivation{
		request: req, runtime: runtime, state: state,
	})
	r.threads[threadID] = binding
	r.conversations[req.Ref.ConversationID] = threadID
	return binding, nil
}

// bindingDuringWriterLease 允许只读探测返回同一租约快照，但禁止改写运行代次。
func (r *codexRuntimeOwnerRegistry) bindingDuringWriterLease(req CodexRuntimeRequest, runtime CodexRuntimeHolder) (CodexThreadBinding, error) {
	binding, ok := r.threads[req.Ref.ThreadID]
	if !ok || !sameCodexControlIntent(binding.Control, req.Intent) {
		return CodexThreadBinding{}, ErrCodexControlChanged
	}
	if binding.Runtime == CodexRuntimeConflict {
		return binding, ErrCodexRuntimeConflict
	}
	if binding.Runtime != runtime {
		return binding, ErrCodexWriterBusy
	}
	return binding, nil
}

// beginTurn 原子核对控制 revision、route 和 runtime generation 后创建 writer lease。
func (r *codexRuntimeOwnerRegistry) beginTurn(req CodexRuntimeRequest) (*codexWriterLease, error) {
	if err := validateRemoteCodexRequest(req); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.threads[req.Ref.ThreadID]
	if !ok || !sameCodexControlIntent(binding.Control, req.Intent) {
		return nil, ErrCodexControlChanged
	}
	if binding.Runtime == CodexRuntimeConflict {
		return nil, ErrCodexRuntimeConflict
	}
	if binding.Runtime != CodexRuntimeDesktop && binding.Runtime != CodexRuntimeWeClaw {
		return nil, ErrCodexRuntimeUnavailable
	}
	if r.leases[req.Ref.ThreadID] != nil {
		return nil, ErrCodexWriterBusy
	}
	state := &codexWriterLeaseState{
		runtimeGeneration: binding.RuntimeGeneration, controlRevision: req.Intent.Revision,
		runtime: binding.Runtime, routeKey: req.Intent.RouteKey,
		baselineLastTurnID: strings.TrimSpace(binding.State.LastTurnID), conflictCh: make(chan struct{}),
	}
	r.leases[req.Ref.ThreadID] = state
	return &codexWriterLease{registry: r, threadID: req.Ref.ThreadID, state: state}, nil
}

func (r *codexRuntimeOwnerRegistry) hasWriterLease(threadID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leases[strings.TrimSpace(threadID)] != nil
}

// accept 把实际 turn ID 绑定到租约，并核对启动响应前到达的 Desktop 快照。
func (l *codexWriterLease) accept(turnID string) error {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return fmt.Errorf("Codex turn ID 不能为空")
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leases[l.threadID] != l.state {
		return ErrCodexControlChanged
	}
	if err := l.bindingErrorLocked(); err != nil {
		return err
	}
	if l.state.candidateDesktopTurn != "" && l.state.candidateDesktopTurn != turnID {
		return r.markConflictLocked(l.threadID, "Desktop 在远程 turn 启动期间开始了其他任务")
	}
	l.state.turnID = turnID
	binding := r.threads[l.threadID]
	binding.State.Active = true
	binding.State.ActiveTurnID = turnID
	r.threads[l.threadID] = binding
	return nil
}

func (l *codexWriterLease) check() error {
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leases[l.threadID] != l.state {
		return ErrCodexControlChanged
	}
	if l.state.conflict || r.threads[l.threadID].Runtime == CodexRuntimeConflict {
		return ErrCodexRuntimeConflict
	}
	return l.bindingErrorLocked()
}

// bindingErrorLocked 核对租约创建后的控制 revision、route 与运行代次没有变化。
func (l *codexWriterLease) bindingErrorLocked() error {
	binding := l.registry.threads[l.threadID]
	if binding.Control.Owner != CodexControlRemote || binding.Control.Revision != l.state.controlRevision ||
		binding.Control.RouteKey != l.state.routeKey {
		return ErrCodexControlChanged
	}
	if binding.Runtime != l.state.runtime || binding.RuntimeGeneration != l.state.runtimeGeneration {
		return ErrCodexRuntimeUnavailable
	}
	return nil
}

func (l *codexWriterLease) conflictSignal() <-chan struct{} {
	return l.state.conflictCh
}

func (l *codexWriterLease) finish() {
	if l == nil || l.registry == nil {
		return
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leases[l.threadID] != l.state {
		return
	}
	delete(r.leases, l.threadID)
	binding := r.threads[l.threadID]
	if binding.Runtime != CodexRuntimeConflict {
		binding.State.Active = false
		binding.State.ActiveTurnID = ""
	}
	r.threads[l.threadID] = binding
}

func activateCodexBinding(current CodexThreadBinding, activation codexBindingActivation) CodexThreadBinding {
	req := activation.request
	runtime := activation.runtime
	generation := current.RuntimeGeneration
	if generation == 0 || current.Runtime != runtime {
		generation++
	}
	state := activation.state
	state.ThreadID = req.Ref.ThreadID
	binding := CodexThreadBinding{
		Ref: req.Ref, Control: req.Intent, Runtime: runtime,
		RuntimeGeneration: generation, State: state,
	}
	if runtime == CodexRuntimeConflict {
		binding.ConflictReason = current.ConflictReason
	}
	return binding
}

func validateCodexRuntimeRequest(req CodexRuntimeRequest) error {
	if strings.TrimSpace(req.Ref.ThreadID) == "" || strings.TrimSpace(req.Ref.ConversationID) == "" {
		return fmt.Errorf("Codex runtime 请求缺少 thread 或 conversation")
	}
	switch req.Intent.Owner {
	case CodexControlUnclaimed, CodexControlDesktop:
		return nil
	case CodexControlRemote:
		if strings.TrimSpace(req.Intent.RouteKey) == "" || strings.TrimSpace(req.Intent.ConversationID) == "" {
			return ErrCodexControlRequired
		}
		return nil
	default:
		return fmt.Errorf("无效的 Codex 控制方 %q", req.Intent.Owner)
	}
}

func validateRemoteCodexRequest(req CodexRuntimeRequest) error {
	if err := validateCodexRuntimeRequest(req); err != nil {
		return err
	}
	if req.Intent.Owner != CodexControlRemote ||
		strings.TrimSpace(req.Intent.ConversationID) != strings.TrimSpace(req.Ref.ConversationID) {
		return ErrCodexControlRequired
	}
	return nil
}

func sameCodexControlIntent(left CodexControlIntent, right CodexControlIntent) bool {
	return left.Owner == right.Owner && left.RouteKey == right.RouteKey &&
		left.ConversationID == right.ConversationID && left.Revision == right.Revision
}

func validCodexRuntimeHolder(runtime CodexRuntimeHolder) bool {
	return runtime == CodexRuntimeUnknown || runtime == CodexRuntimeDesktop ||
		runtime == CodexRuntimeWeClaw || runtime == CodexRuntimeConflict
}
