package agent

import (
	"context"
	"fmt"
)

type codexLeasedTurnOptions struct {
	binding CodexThreadBinding
	request CodexTurnRequest
	lease   *codexWriterLease
}

// RunCodexTurn 在最终 Desktop 探测后持有 writer lease，直到 turn 到达终态。
func (a *ACPAgent) RunCodexTurn(ctx context.Context, req CodexTurnRequest) (string, error) {
	binding, err := a.InspectCodexRuntime(ctx, req.Runtime)
	if err != nil {
		return "", err
	}
	lease, err := a.codexOwners.beginTurn(req.Runtime)
	if err != nil {
		return "", err
	}
	defer lease.finish()
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cancelCodexTurnOnConflict(turnCtx, cancel, lease.conflictSignal())

	reply, runErr := a.runCodexTurnWithLease(turnCtx, codexLeasedTurnOptions{
		binding: binding, request: req, lease: lease,
	})
	if leaseErr := lease.check(); leaseErr != nil {
		return "", leaseErr
	}
	return reply, runErr
}

func (a *ACPAgent) runCodexTurnWithLease(ctx context.Context, opts codexLeasedTurnOptions) (string, error) {
	req := opts.request
	switch opts.binding.Runtime {
	case CodexRuntimeDesktop:
		return a.chatCodexDesktopTurn(codexDesktopTurnOptions{
			ctx: ctx, binding: opts.binding, message: req.Message,
			onProgress: req.OnProgress, onStarted: opts.lease.accept,
		})
	case CodexRuntimeWeClaw:
		return a.chatCodexAppServerControlledTurn(codexAppServerTurnOptions{
			ctx: ctx, conversationID: req.Runtime.Ref.ConversationID,
			message: req.Message, onProgress: req.OnProgress, onStarted: opts.lease.accept,
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
