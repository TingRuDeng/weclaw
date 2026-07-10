package messaging

import (
	"context"
	"path/filepath"
)

type workspaceAdminContextKey struct{}

func contextWithWorkspaceAdmin(ctx context.Context, admin bool) context.Context {
	if !admin {
		return ctx
	}
	return context.WithValue(ctx, workspaceAdminContextKey{}, true)
}

func workspaceAdminFromContext(ctx context.Context) bool {
	admin, _ := ctx.Value(workspaceAdminContextKey{}).(bool)
	return admin
}

func (h *Handler) workspaceAllowedForAgentContext(ctx context.Context, agentName string, workspaceRoot string) bool {
	return workspaceAdminFromContext(ctx) || h.isWorkspaceAllowed(workspaceRoot) || h.isConfiguredAgentWorkspace(agentName, workspaceRoot)
}

func (h *Handler) isConfiguredAgentWorkspace(agentName string, workspaceRoot string) bool {
	normalized := filepath.Clean(workspaceRoot)
	if normalized == filepath.Clean(defaultAttachmentWorkspace()) {
		return true
	}
	h.mu.RLock()
	configured := h.configuredAgentWorkDirs[agentName]
	h.mu.RUnlock()
	return configured != "" && normalized == filepath.Clean(configured)
}

func (h *Handler) isConfiguredWorkspace(workspaceRoot string) bool {
	normalized := filepath.Clean(workspaceRoot)
	if normalized == filepath.Clean(defaultAttachmentWorkspace()) {
		return true
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, configured := range h.configuredAgentWorkDirs {
		if configured != "" && normalized == filepath.Clean(configured) {
			return true
		}
	}
	return false
}
