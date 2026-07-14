package messaging

import (
	"context"
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
	normalized, ok := canonicalWorkspacePath(workspaceRoot)
	if !ok {
		return false
	}
	defaultWorkspace, _ := canonicalWorkspacePath(defaultAttachmentWorkspace())
	if normalized == defaultWorkspace {
		return true
	}
	h.mu.RLock()
	configured := h.configuredAgentWorkDirs[agentName]
	h.mu.RUnlock()
	configuredPath, ok := canonicalWorkspacePath(configured)
	return ok && normalized == configuredPath
}

func (h *Handler) isConfiguredWorkspace(workspaceRoot string) bool {
	normalized, ok := canonicalWorkspacePath(workspaceRoot)
	if !ok {
		return false
	}
	defaultWorkspace, _ := canonicalWorkspacePath(defaultAttachmentWorkspace())
	if normalized == defaultWorkspace {
		return true
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, configured := range h.configuredAgentWorkDirs {
		configuredPath, valid := canonicalWorkspacePath(configured)
		if valid && normalized == configuredPath {
			return true
		}
	}
	return false
}

// canonicalWorkspacePath 统一配置路径和会话真实路径的比较口径。
func canonicalWorkspacePath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	canonical, err := canonicalizePath(path, false)
	return canonical, err == nil
}
