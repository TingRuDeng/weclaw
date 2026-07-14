package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

type codexSessionCommandRuntime struct {
	ctx             context.Context
	req             codexSessionCommandRequest
	actorUserID     string
	routeUserID     string
	admin           bool
	fields          []string
	agentName       string
	agent           agent.Agent
	bindingKey      string
	ownerBindingKey string
	workspaceRoot   string
}

type codexSessionCommandPreparation struct {
	runtime codexSessionCommandRuntime
	result  navigationCommandResult
	unlock  func()
	ready   bool
}

// prepareCodexSessionCommand 解析路由并在同一绑定锁内准备命令运行状态。
func (h *Handler) prepareCodexSessionCommand(ctx context.Context, req codexSessionCommandRequest) codexSessionCommandPreparation {
	actorUserID, routeUserID := normalizeCodexCommandUsers(req)
	fields := strings.Fields(req.Trimmed)
	if len(fields) < 2 || fields[1] == "help" {
		return codexSessionCommandPreparation{result: textNavigationResult(buildCodexSessionHelpText())}
	}
	if fields[1] == "model" && isCodexModelStatusArgs(fields[2:]) {
		return codexSessionCommandPreparation{result: textNavigationResult(h.renderCodexModelStatusFromConfig())}
	}
	agentName, ag, err := h.getCodexSessionAgent(ctx)
	if err != nil {
		return codexSessionCommandPreparation{result: textNavigationResult(err.Error())}
	}
	runtime := codexSessionCommandRuntime{
		ctx: ctx, req: req, actorUserID: actorUserID, routeUserID: routeUserID,
		admin: req.Admin || h.isAdminUser(actorUserID), fields: fields,
		agentName: agentName, agent: ag,
		bindingKey:      codexBindingKey(routeUserID, agentName),
		ownerBindingKey: codexBindingKey(actorUserID, agentName),
		workspaceRoot:   h.codexWorkspaceRootForRoute(actorUserID, routeUserID, agentName, ag),
	}
	unlock := h.lockAgentExecution(codexBindingExecutionKey(runtime.bindingKey))
	if reply := h.rejectDisallowedCodexWorkspace(runtime.bindingKey, agentName, runtime.workspaceRoot, fields, runtime.admin); reply != "" {
		unlock()
		return codexSessionCommandPreparation{result: textNavigationResult(reply)}
	}
	h.ensureCodexSessions().ensureWorkspace(runtime.bindingKey, runtime.workspaceRoot)
	h.syncCodexThreadFromAgent(routeUserID, agentName, runtime.workspaceRoot, ag)
	return codexSessionCommandPreparation{runtime: runtime, unlock: unlock, ready: true}
}

// normalizeCodexCommandUsers 补齐真实用户和平台路由用户。
func normalizeCodexCommandUsers(req codexSessionCommandRequest) (string, string) {
	actorUserID := strings.TrimSpace(req.ActorUserID)
	routeUserID := strings.TrimSpace(req.RouteUserID)
	if routeUserID == "" {
		routeUserID = actorUserID
	}
	if actorUserID == "" {
		actorUserID = routeUserID
	}
	return actorUserID, routeUserID
}

// dispatchCodexSessionCommand 按职责分组分发命令，保持每个分支函数可独立验证。
func (h *Handler) dispatchCodexSessionCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	if result, handled := h.dispatchCodexNavigationCommand(runtime); handled {
		return result
	}
	if result, handled := h.dispatchCodexUtilityCommand(runtime); handled {
		return result
	}
	return h.dispatchCodexMutationCommand(runtime)
}

// dispatchCodexNavigationCommand 处理只读信息和工作空间导航命令。
func (h *Handler) dispatchCodexNavigationCommand(runtime codexSessionCommandRuntime) (navigationCommandResult, bool) {
	fields := runtime.fields
	if len(fields) == 2 && isCodexShortSelectionToken(fields[1]) {
		return h.handleCodexShortSelection(runtime.ctx, runtime.shortSelectionRequest()), true
	}
	switch fields[1] {
	case "whoami":
		return textNavigationResult(h.renderCodexWhoami(runtime.bindingKey, runtime.workspaceRoot)), true
	case "ls":
		return cardNavigationResult(h.renderCodexListForAccess(runtime.bindingKey, runtime.actorUserID, runtime.admin)), true
	case "cd":
		if len(fields) != 3 {
			return textNavigationResult("用法: /cx cd <编号|工作空间名|..>"), true
		}
		return h.handleCodexCdResult(runtime.workspaceCdRequest(fields[2])), true
	case "pwd":
		return textNavigationResult(h.renderCodexPwd(runtime.bindingKey)), true
	default:
		return navigationCommandResult{}, false
	}
}

// dispatchCodexUtilityCommand 处理状态、配额和本地接管工具命令。
func (h *Handler) dispatchCodexUtilityCommand(runtime codexSessionCommandRuntime) (navigationCommandResult, bool) {
	fields := runtime.fields
	switch fields[1] {
	case "owner":
		return h.handleCodexOwnerCommand(runtime), true
	case "status":
		if len(fields) != 2 {
			return textNavigationResult("用法: /cx status"), true
		}
		return textNavigationResult(h.renderCodexStatusForRoute(runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)), true
	case "quota":
		return h.codexNoArgCommandResult(fields, "/cx quota", func() string { return h.renderCodexQuota(runtime.ctx, runtime.agent) })
	case "clean":
		return h.codexNoArgCommandResult(fields, "/cx clean", func() string { return h.handleCodexClean(runtime.bindingKey) })
	case "app":
		return h.codexNoArgCommandResult(fields, "/cx app", func() string {
			return h.handleCodexOpenAppForRoute(runtime.ctx, runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)
		})
	case "cli":
		return h.codexNoArgCommandResult(fields, "/cx cli", func() string {
			return h.handleCodexCLIForRoute(runtime.ctx, runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)
		})
	case "attach":
		return h.codexNoArgCommandResult(fields, "/cx attach", func() string {
			return h.handleCodexAttachForRoute(runtime.ctx, runtime.actorUserID, runtime.routeUserID, runtime.agentName, runtime.agent)
		})
	case "detach":
		return h.codexNoArgCommandResult(fields, "/cx detach", func() string { return h.handleCodexDetach(runtime.agent) })
	default:
		return navigationCommandResult{}, false
	}
}

// codexNoArgCommandResult 统一校验无参数命令并返回纯文本结果。
func (h *Handler) codexNoArgCommandResult(fields []string, usage string, run func() string) (navigationCommandResult, bool) {
	if len(fields) != 2 {
		return textNavigationResult("用法: " + usage), true
	}
	return textNavigationResult(run()), true
}

// dispatchCodexMutationCommand 处理模型、新建和切换会话命令。
func (h *Handler) dispatchCodexMutationCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	fields := runtime.fields
	switch fields[1] {
	case "model":
		return textNavigationResult(h.handleCodexModelCommand(runtime.ctx, runtime.agent, fields[2:]))
	case "new":
		return textNavigationResult(h.handleCodexNewForRoute(codexNewRequest{
			ctx: runtime.ctx, userID: runtime.routeUserID, agentName: runtime.agentName,
			workspaceRoot: runtime.workspaceRoot, agent: runtime.agent, ownerBindingKey: runtime.ownerBindingKey,
		}))
	case "switch":
		return h.dispatchCodexSwitchCommand(runtime)
	default:
		return textNavigationResult(buildCodexSessionHelpText())
	}
}

// dispatchCodexSwitchCommand 校验并执行会话切换命令。
func (h *Handler) dispatchCodexSwitchCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	if len(runtime.fields) != 3 {
		return textNavigationResult("用法: /cx switch <编号|threadId>")
	}
	text := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: runtime.ctx, userID: runtime.routeUserID, agentName: runtime.agentName,
		workspaceRoot: runtime.workspaceRoot, agent: runtime.agent,
		target: runtime.fields[2], ownerBindingKey: runtime.ownerBindingKey,
		options: codexSwitchOptions{
			actorUserID: runtime.actorUserID, platform: runtime.req.Platform,
			accountID: runtime.req.AccountID, reply: runtime.req.Reply,
		},
	})
	return textNavigationResult(text)
}

// shortSelectionRequest 构造短编号选择请求。
func (runtime codexSessionCommandRuntime) shortSelectionRequest() codexShortSelectionRequest {
	return codexShortSelectionRequest{
		UserID: runtime.routeUserID, ActorUserID: runtime.actorUserID,
		AgentName: runtime.agentName, WorkspaceRoot: runtime.workspaceRoot,
		Agent: runtime.agent, BindingKey: runtime.bindingKey, Target: runtime.fields[1],
		OwnerBindingKey: runtime.ownerBindingKey, Platform: runtime.req.Platform,
		AccountID: runtime.req.AccountID, Reply: runtime.req.Reply, Admin: runtime.admin,
	}
}

// workspaceCdRequest 构造工作空间切换请求。
func (runtime codexSessionCommandRuntime) workspaceCdRequest(target string) codexWorkspaceCdRequest {
	return codexWorkspaceCdRequest{
		Context: runtime.ctx, UserID: runtime.routeUserID, ActorUserID: runtime.actorUserID,
		BindingKey: runtime.bindingKey, OwnerBindingKey: runtime.ownerBindingKey,
		AgentName: runtime.agentName, Target: target, Agent: runtime.agent,
		Platform: runtime.req.Platform, AccountID: runtime.req.AccountID,
		Reply: runtime.req.Reply, Admin: runtime.admin,
	}
}
