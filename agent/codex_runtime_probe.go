package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InspectCodexRuntime 每次重新探测 Desktop，并同步已持久化的用户控制意图。
func (a *ACPAgent) InspectCodexRuntime(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	runtime, state, err := a.probeCodexRuntime(ctx, req, codexRuntimeProbeOptions{})
	binding, activateErr := a.codexOwners.activateRuntime(req, runtime, state)
	if activateErr != nil {
		return binding, activateErr
	}
	return binding, err
}

// CurrentCodexRuntime 返回已建立的 runtime 绑定，不向 Desktop 发起同步探测。
func (a *ACPAgent) CurrentCodexRuntime(req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID)
	if !ok {
		return unknownCodexRuntimeSnapshot(req, CodexThreadState{}), nil
	}
	if !sameCodexControlIntent(binding.Control, req.Intent) {
		if a.codexOwners.hasWriterLease(req.Ref.ThreadID) {
			return binding, ErrCodexControlChanged
		}
		return unknownCodexRuntimeSnapshot(req, binding.State), nil
	}
	binding.Ref = req.Ref
	return binding, nil
}

func unknownCodexRuntimeSnapshot(req CodexRuntimeRequest, state CodexThreadState) CodexThreadBinding {
	state.ThreadID = req.Ref.ThreadID
	return CodexThreadBinding{
		Ref: req.Ref, Control: req.Intent, Runtime: CodexRuntimeUnknown, State: state,
	}
}

// HandoffCodexRuntime 执行用户已明确选择的控制权移交，不替用户自动决定控制方。
func (a *ACPAgent) HandoffCodexRuntime(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return CodexThreadBinding{}, err
	}
	if a.codexOwners.hasWriterLease(req.Ref.ThreadID) {
		return CodexThreadBinding{}, ErrCodexWriterBusy
	}
	if req.Intent.Owner == CodexControlUnclaimed {
		return a.codexOwners.activateRuntime(req, CodexRuntimeUnknown, CodexThreadState{ThreadID: req.Ref.ThreadID})
	}
	runtime, state, err := a.probeCodexRuntime(ctx, req, codexRuntimeProbeOptions{allowConflictRecovery: true})
	if err != nil && !(req.Intent.Owner == CodexControlDesktop && runtime == CodexRuntimeConflict) {
		return CodexThreadBinding{}, err
	}
	if req.Intent.Owner == CodexControlDesktop && runtime == CodexRuntimeConflict {
		runtime = CodexRuntimeDesktop
	}
	if req.Intent.Owner == CodexControlDesktop || runtime != CodexRuntimeUnknown {
		return a.codexOwners.activateRuntime(req, runtime, state)
	}
	return a.recoverCodexRuntimeForRemote(ctx, req)
}

// MarkCodexRuntimeConflict 将无法确认 writer 的 thread 持续登记为冲突态。
func (a *ACPAgent) MarkCodexRuntimeConflict(ctx context.Context, req CodexRuntimeRequest) error {
	if err := a.validateCodexRuntimeSupport(req); err != nil {
		return err
	}
	_ = ctx
	a.codexOwners.markRuntimeConflict(req, "控制权移交结果未确认")
	return nil
}

func (a *ACPAgent) validateCodexRuntimeSupport(req CodexRuntimeRequest) error {
	if a.protocol != protocolCodexAppServer || a.codexOwners == nil {
		return ErrCodexRuntimeUnavailable
	}
	return validateCodexRuntimeRequest(req)
}

type codexRuntimeProbeOptions struct {
	allowConflictRecovery bool
}

// probeCodexRuntime 只把完整 Desktop 快照或明确无人处理视为确定性结论。
func (a *ACPAgent) probeCodexRuntime(ctx context.Context, req CodexRuntimeRequest, opts codexRuntimeProbeOptions) (CodexRuntimeHolder, CodexThreadState, error) {
	current, _ := a.codexOwners.threadBinding(req.Ref.ThreadID)
	if a.desktopProbe == nil {
		return CodexRuntimeUnknown, current.State, ErrCodexDesktopOwnershipUnknown
	}
	_, discoverErr := a.desktopProbe.Discover(ctx, req.Ref)
	loadErr := a.desktopProbe.LoadHistory(ctx, req.Ref)
	if binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID); ok {
		if binding.Runtime == CodexRuntimeConflict {
			if opts.allowConflictRecovery && desktopReleaseConfirmed(a.desktopProbe, loadErr) {
				return CodexRuntimeUnknown, binding.State, nil
			}
			return CodexRuntimeConflict, binding.State, ErrCodexRuntimeConflict
		}
		if binding.Runtime == CodexRuntimeDesktop {
			return CodexRuntimeDesktop, binding.State, nil
		}
	}
	if desktopReleaseConfirmed(a.desktopProbe, loadErr) {
		if current.Runtime == CodexRuntimeWeClaw {
			return CodexRuntimeWeClaw, current.State, nil
		}
		return CodexRuntimeUnknown, current.State, nil
	}
	return CodexRuntimeUnknown, current.State, codexProbeError(discoverErr, loadErr)
}

func desktopReleaseConfirmed(probe codexDesktopOwnerProbe, loadErr error) bool {
	if errors.Is(loadErr, ErrCodexDesktopNoClient) {
		return true
	}
	socketExists, processExists := probe.Presence()
	return !socketExists && !processExists
}

func codexProbeError(discoverErr error, loadErr error) error {
	if loadErr != nil {
		return fmt.Errorf("%w: %v", ErrCodexDesktopOwnershipUnknown, loadErr)
	}
	if discoverErr != nil {
		return fmt.Errorf("%w: %v", ErrCodexDesktopOwnershipUnknown, discoverErr)
	}
	return ErrCodexDesktopOwnershipUnknown
}

func (a *ACPAgent) recoverCodexRuntimeForRemote(ctx context.Context, req CodexRuntimeRequest) (CodexThreadBinding, error) {
	if err := validateCodexRolloutCheckpoint(req.Checkpoint); err != nil {
		return CodexThreadBinding{}, err
	}
	if err := a.restartCodexAppServer(ctx); err != nil {
		return CodexThreadBinding{}, err
	}
	if err := a.resumeThread(ctx, req.Ref.ConversationID, req.Ref.ThreadID); err != nil {
		return CodexThreadBinding{}, fmt.Errorf("恢复 Codex thread 失败: %w", err)
	}
	if err := validateCodexRolloutCheckpoint(req.Checkpoint); err != nil {
		return CodexThreadBinding{}, err
	}
	state, err := a.readCodexAppServerThreadState(ctx, req.Ref.ThreadID)
	if err != nil {
		return CodexThreadBinding{}, err
	}
	if checkpointTurnChanged(req.Checkpoint, state) {
		return CodexThreadBinding{}, ErrCodexCheckpointChanged
	}
	a.mu.Lock()
	a.threads[req.Ref.ConversationID] = req.Ref.ThreadID
	delete(a.resumeOnFirstUse, req.Ref.ConversationID)
	a.mu.Unlock()
	binding, err := a.codexOwners.activateRuntime(req, CodexRuntimeWeClaw, state)
	if err == nil {
		a.persistState()
	}
	return binding, err
}

func validateCodexRolloutCheckpoint(checkpoint CodexRolloutCheckpoint) error {
	path := strings.TrimSpace(checkpoint.Path)
	if path == "" || !filepath.IsAbs(path) || checkpoint.Offset < 0 || checkpoint.Size != checkpoint.Offset || checkpoint.Active {
		return ErrCodexCheckpointRequired
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取 Codex rollout checkpoint: %w", err)
	}
	if info.Size() != checkpoint.Size {
		return ErrCodexCheckpointChanged
	}
	return nil
}

func checkpointTurnChanged(checkpoint CodexRolloutCheckpoint, state CodexThreadState) bool {
	turnID := strings.TrimSpace(checkpoint.TurnID)
	return turnID != "" && turnID != strings.TrimSpace(state.LastTurnID)
}

func (a *ACPAgent) readCodexAppServerThreadState(ctx context.Context, threadID string) (CodexThreadState, error) {
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{
		"threadId": strings.TrimSpace(threadID), "includeTurns": true,
	})
	if err != nil {
		return CodexThreadState{}, err
	}
	var response codexThreadReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return CodexThreadState{}, fmt.Errorf("parse thread/read result: %w", err)
	}
	return codexThreadStateFromSnapshot(response.Thread), nil
}
