package agent

import (
	"context"
	"errors"
	"fmt"
)

type codexLeasedTurnOptions struct {
	binding CodexThreadBinding
	request CodexTurnRequest
	lease   *codexWriterLease
}

// RunCodexTurn 使用已建立的 runtime 绑定并持有 writer lease，直到 turn 到达终态。
func (a *ACPAgent) RunCodexTurn(ctx context.Context, req CodexTurnRequest) (string, error) {
	var binding CodexThreadBinding
	var err error
	if a.desktopProbe == nil {
		if a.usesCodexSharedHost() {
			if err := a.ensureStarted(ctx); err != nil {
				return "", err
			}
		}
		if err := a.ensureCodexAccountForTurn(ctx); err != nil {
			return "", err
		}
		// Every frontend conversation has its own app-server mapping. Rebind on
		// each admitted turn so a binding created while another client held the
		// thread lease cannot accidentally reuse a stale conversation mapping.
		binding, err = a.activateSharedCodexHost(ctx, req.Runtime)
	} else {
		binding, err = a.CurrentCodexRuntime(req.Runtime)
	}
	if err != nil {
		return "", err
	}
	if a.desktopProbe != nil && (binding.Runtime == CodexRuntimeUnknown || binding.Runtime == CodexRuntimeConflict) {
		binding, err = a.HandoffCodexRuntime(ctx, req.Runtime)
		if err != nil {
			req, binding, err = a.replaceMissingFirstTurnThread(ctx, req, err)
			if err != nil {
				return "", err
			}
		}
	}
	if a.desktopProbe == nil && binding.State.Active {
		return "", ErrCodexWriterBusy
	}
	lease, err := a.codexOwners.beginTurn(req.Runtime)
	if err != nil {
		return "", err
	}
	retainLease := false
	defer func() {
		if !retainLease {
			lease.finish()
		}
	}()
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cancelCodexTurnOnConflict(turnCtx, cancel, lease.conflictSignal())

	reply, runErr := a.runCodexTurnWithLease(turnCtx, codexLeasedTurnOptions{
		binding: binding, request: req, lease: lease,
	})
	if leaseErr := lease.check(); leaseErr != nil {
		return "", leaseErr
	}
	var interrupted *CodexTurnInterruptedError
	if errors.As(runErr, &interrupted) {
		lease.markUncertain()
		interrupted.setTerminalConfirmation(lease.finish)
		retainLease = true
	}
	return reply, runErr
}

// replaceMissingFirstTurnThread 仅在 app-server 明确确认旧 thread 不存在且外层允许首写补建时创建替代 thread。
func (a *ACPAgent) replaceMissingFirstTurnThread(
	ctx context.Context,
	req CodexTurnRequest,
	recoveryErr error,
) (CodexTurnRequest, CodexThreadBinding, error) {
	if !req.Runtime.PendingFirstTurn || req.OnThreadReplaced == nil || !isMissingThreadError(recoveryErr) {
		return req, CodexThreadBinding{}, recoveryErr
	}
	previous := req.Runtime.Ref
	threadID, err := a.createThread(ctx, previous.ConversationID)
	if err != nil {
		return req, CodexThreadBinding{}, fmt.Errorf("补建 Codex 首次写入 thread: %w", err)
	}
	current := CodexThreadRef{ConversationID: previous.ConversationID, ThreadID: threadID}
	if err := req.OnThreadReplaced(previous, current); err != nil {
		return req, CodexThreadBinding{}, fmt.Errorf("提交 Codex 首次写入 thread 替换: %w", err)
	}
	req.Runtime.Ref = current
	req.Runtime.Checkpoint = CodexRolloutCheckpoint{}
	req.Runtime.PendingFirstTurn = true
	binding, err := a.codexOwners.activateRuntime(
		req.Runtime, CodexRuntimeWeClaw, CodexThreadState{ThreadID: threadID},
	)
	if err != nil {
		return req, binding, err
	}
	a.persistState()
	return req, binding, nil
}

func (a *ACPAgent) runCodexTurnWithLease(ctx context.Context, opts codexLeasedTurnOptions) (string, error) {
	req := opts.request
	onStarted := func(turnID string) error {
		if err := opts.lease.accept(turnID); err != nil {
			return err
		}
		if req.OnTurnStarted != nil {
			return req.OnTurnStarted(req.Runtime.Ref, turnID)
		}
		return nil
	}
	if a.desktopProbe == nil {
		return a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
			ctx: ctx, conversationID: req.Runtime.Ref.ConversationID,
			message: req.Message, onProgress: req.OnProgress, onProgressEvent: req.OnProgressEvent, onStarted: onStarted,
		})
	}
	switch opts.binding.Runtime {
	case CodexRuntimeDesktop:
		return a.chatCodexDesktopTurn(codexDesktopTurnOptions{
			ctx: ctx, binding: opts.binding, message: req.Message,
			onProgress: req.OnProgress, onProgressEvent: req.OnProgressEvent, onStarted: onStarted,
		})
	case CodexRuntimeWeClaw:
		return a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
			ctx: ctx, conversationID: req.Runtime.Ref.ConversationID,
			message: req.Message, onProgress: req.OnProgress, onProgressEvent: req.OnProgressEvent, onStarted: onStarted,
		})
	case CodexRuntimeConflict:
		return "", ErrCodexRuntimeConflict
	default:
		return "", fmt.Errorf("%w: %s", ErrCodexRuntimeUnavailable, opts.binding.Runtime)
	}
}

func cancelCodexTurnOnConflict(ctx context.Context, cancel context.CancelFunc, conflict <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-conflict:
		cancel()
	}
}
