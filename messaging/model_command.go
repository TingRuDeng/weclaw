package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

const (
	modelSettingModel     = "model"
	modelSettingReasoning = "reasoning"
)

type modelAgentRoute struct {
	routeUserID string
	platform    platform.PlatformName
	accountID   string
}

type modelSettingCardRequest struct {
	message platform.IncomingMessage
	reply   platform.Replier
	setting string
	route   modelAgentRoute
}

func isModelSettingCommand(command string) bool {
	return command == "/model" || strings.HasPrefix(command, "/model ") ||
		command == "/reasoning" || strings.HasPrefix(command, "/reasoning ")
}

// handleModelSettingPlatformCommand 统一模型设置文本命令和飞书卡片入口。
func (h *Handler) handleModelSettingPlatformCommand(ctx context.Context, req platformCommandRequest) bool {
	msg := req.Message
	setting, prefix := modelSettingModel, "/model"
	if strings.HasPrefix(req.Trimmed, "/reasoning") {
		setting, prefix = modelSettingReasoning, "/reasoning"
	}
	arg := strings.TrimSpace(strings.TrimPrefix(req.Trimmed, prefix))
	route := modelAgentRoute{routeUserID: req.RouteUserID, platform: msg.Platform, accountID: msg.AccountID}
	if arg == "" && h.sendFeishuModelSettingCard(ctx, modelSettingCardRequest{
		message: msg,
		reply:   req.Reply,
		setting: setting,
		route:   route,
	}) {
		return true
	}
	var text string
	if setting == modelSettingReasoning {
		text = h.handleReasoningCommandForRoute(ctx, route, arg)
	} else {
		text = h.handleModelCommandForRoute(ctx, route, arg)
	}
	sendPlatformText(ctx, req.Reply, msg.UserID, text)
	return true
}

// handleModelCommand 统一的 /model 入口：查看/切换当前会话 Agent 的模型。
func (h *Handler) handleModelCommand(ctx context.Context, platformName platform.PlatformName, arg string) string {
	return h.handleModelCommandForAccount(ctx, platformName, "", arg)
}

func (h *Handler) handleModelCommandForAccount(ctx context.Context, platformName platform.PlatformName, accountID string, arg string) string {
	return h.handleModelCommandForRoute(ctx, modelAgentRoute{platform: platformName, accountID: accountID}, arg)
}

func (h *Handler) handleModelCommandForRoute(ctx context.Context, route modelAgentRoute, arg string) string {
	name, ag, ok := h.resolveModelAgentForRoute(ctx, route)
	if !ok {
		return "当前没有可用的默认 Agent。"
	}
	control, ok := newModelSettingController(name, ag)
	if !ok {
		return modelFixedByConfigHint(name)
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return renderModelOverview(ctx, control)
	}
	control.SetModel(arg, "")
	return wechatCommandText(
		"已将 "+name+" 模型切换为: "+arg,
		"将在下一个新会话生效，发送 /new 立即开新会话使用。",
	)
}

// handleReasoningCommand 统一的 /reasoning 入口：查看/切换默认 agent 的推理强度。
func (h *Handler) handleReasoningCommand(ctx context.Context, platformName platform.PlatformName, arg string) string {
	return h.handleReasoningCommandForAccount(ctx, platformName, "", arg)
}

func (h *Handler) handleReasoningCommandForAccount(ctx context.Context, platformName platform.PlatformName, accountID string, arg string) string {
	return h.handleReasoningCommandForRoute(ctx, modelAgentRoute{platform: platformName, accountID: accountID}, arg)
}

func (h *Handler) handleReasoningCommandForRoute(ctx context.Context, route modelAgentRoute, arg string) string {
	name, ag, ok := h.resolveModelAgentForRoute(ctx, route)
	if !ok {
		return "当前没有可用的默认 Agent。"
	}
	control, ok := newModelSettingController(name, ag)
	if !ok {
		return modelFixedByConfigHint(name)
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return renderReasoningOverview(ctx, control)
	}
	control.SetModel("", arg)
	return wechatCommandText(
		"已将 "+name+" 推理强度切换为: "+arg,
		"将在下一个新会话生效，发送 /new 立即开新会话使用。",
	)
}

func (h *Handler) resolveModelAgentForRoute(ctx context.Context, route modelAgentRoute) (string, agent.Agent, bool) {
	name := h.defaultAgentNameForRoute(route.routeUserID, route.platform, route.accountID)
	if strings.TrimSpace(name) == "" {
		return "", nil, false
	}
	ag, err := h.getAgent(ctx, name)
	if err != nil || ag == nil {
		return "", nil, false
	}
	return name, ag, true
}
