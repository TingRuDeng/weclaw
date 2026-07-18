package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const codexSessionAcquireCleanupTimeout = 3 * time.Second

// bindCodexSharedRuntime ensures this frontend conversation maps to the
// selected thread on the one shared app-server. A transport failure does not
// roll back the durable frontend binding and is never promoted to a writer
// conflict without authoritative server evidence.
func (h *Handler) bindCodexSharedRuntime(req codexSessionAcquireRequest, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, error) {
	request, rollout, err := h.buildCodexRuntimeRequest(req.route, req.route.threadID)
	if err != nil {
		return codexRuntimeResolution{}, err
	}
	binding, bindErr := liveAgent.HandoffCodexRuntime(req.ctx, request)
	resolution := codexRuntimeResolution{
		Request: request, Binding: binding, Rollout: rollout,
		Live: true, ProbeErr: bindErr,
	}
	if bindErr != nil {
		current, currentErr := liveAgent.CurrentCodexRuntime(request)
		if currentErr == nil {
			resolution.Binding = current
		}
		return resolution, errors.Join(bindErr, currentErr)
	}
	if readyErr := ensureCodexRuntimeReady(resolution, req.route); readyErr != nil {
		return resolution, readyErr
	}
	return resolution, nil
}

func newCodexSessionAcquireCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	parent := context.WithoutCancel(normalizeContext(ctx))
	return context.WithTimeout(parent, codexSessionAcquireCleanupTimeout)
}
