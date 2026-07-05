package agent

import (
	"context"
	"strings"
)

func (a *ACPAgent) approvalPolicyForContext(ctx context.Context) string {
	if policy := strings.TrimSpace(a.approvalPolicy); policy != "" {
		return policy
	}
	return approvalPolicyForContext(ctx)
}

func (a *ACPAgent) sandboxModeForCodex() string {
	mode := strings.TrimSpace(a.sandboxMode)
	if mode == "" {
		return "workspace-write"
	}
	switch strings.ToLower(mode) {
	case "readonly", "read_only", "read-only":
		return "read-only"
	case "workspacewrite", "workspace_write", "workspace-write":
		return "workspace-write"
	case "dangerfullaccess", "danger_full_access", "danger-full-access":
		return "danger-full-access"
	default:
		return mode
	}
}

func (a *ACPAgent) sandboxPolicyTypeForCodex() string {
	switch a.sandboxModeForCodex() {
	case "read-only":
		return "readOnly"
	case "workspace-write":
		return "workspaceWrite"
	case "danger-full-access":
		return "dangerFullAccess"
	default:
		return strings.TrimSpace(a.sandboxMode)
	}
}

// selectPermissionOption 在无法路由给用户时选择保守 fallback，优先拒绝。
func selectPermissionOption(params permissionRequestParams, preferredKind string) string {
	preferredKind = strings.TrimSpace(preferredKind)
	for _, opt := range params.Options {
		if opt.Kind == preferredKind && strings.TrimSpace(opt.OptionID) != "" {
			return opt.OptionID
		}
	}
	for _, decision := range permissionAvailableDecisions(params) {
		if approvalKindFromDecision(decision) == preferredKind && strings.TrimSpace(decision) != "" {
			return strings.TrimSpace(decision)
		}
	}
	for _, opt := range params.Options {
		if opt.Kind != "allow" && strings.TrimSpace(opt.OptionID) != "" {
			return opt.OptionID
		}
	}
	for _, decision := range permissionAvailableDecisions(params) {
		if approvalKindFromDecision(decision) != "allow" && strings.TrimSpace(decision) != "" {
			return strings.TrimSpace(decision)
		}
	}
	if len(params.Options) > 0 {
		return params.Options[0].OptionID
	}
	if decisions := permissionAvailableDecisions(params); len(decisions) > 0 {
		return strings.TrimSpace(decisions[0])
	}
	if preferredKind == "" || preferredKind == "deny" {
		return "decline"
	}
	return preferredKind
}

func selectApprovalOption(options []ApprovalOption, preferredKind string) string {
	for _, opt := range options {
		if opt.Kind == preferredKind && strings.TrimSpace(opt.ID) != "" {
			return opt.ID
		}
	}
	for _, opt := range options {
		if opt.Kind != "allow" && strings.TrimSpace(opt.ID) != "" {
			return opt.ID
		}
	}
	if len(options) > 0 {
		return options[0].ID
	}
	return preferredKind
}

// defaultDenyDecision 在 Codex 新版审批请求缺少 options 时返回协议认可的拒绝值。
func defaultDenyDecision(options []ApprovalOption) string {
	for _, opt := range options {
		if strings.EqualFold(strings.TrimSpace(opt.ID), "cancel") {
			return "cancel"
		}
	}
	return "decline"
}

func isApprovalOption(options []ApprovalOption, optionID string) bool {
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return false
	}
	for _, opt := range options {
		if opt.ID == optionID {
			return true
		}
	}
	return false
}
