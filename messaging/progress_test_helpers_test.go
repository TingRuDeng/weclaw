package messaging

import (
	"context"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func (h *Handler) startProgressSession(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func()) {
	onProgress, finish := h.startProgressSessionWithFinal(ctx, reply, prefix, taskText, cfg)
	return onProgress, func() {
		_ = finish(progressDefaultCompletion, false)
	}
}

func (h *Handler) startProgressSessionWithFinal(ctx context.Context, reply platform.Replier, prefix string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	return h.startProgressSessionForAgentWithFinal(ctx, reply, prefix, "", taskText, cfg)
}

func (h *Handler) startProgressSessionForAgentWithFinal(ctx context.Context, reply platform.Replier, prefix string, agentName string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	return h.startProgressSessionForWorkspaceAgentWithFinal(ctx, reply, prefix, agentName, "", taskText, cfg)
}

func (h *Handler) startProgressSessionForWorkspaceAgentWithFinal(ctx context.Context, reply platform.Replier, prefix string, agentName string, workspaceRoot string, taskText string, cfg config.ProgressConfig) (func(string), func(string, bool) bool) {
	onProgress, finish, _ := h.startProgressSessionForWorkspaceAgentWithHandle(
		ctx, reply, prefix, agentName, workspaceRoot, taskText, cfg,
	)
	return onProgress, finish
}
