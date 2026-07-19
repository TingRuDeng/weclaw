package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

const (
	modelSettingModel                    = "model"
	modelSettingReasoning                = "reasoning"
	modelSettingAgentMetadataKey         = "model_setting_agent"
	modelSettingThreadMetadataKey        = "model_setting_codex_thread"
	modelSettingClaudeSessionMetadataKey = "model_setting_claude_session"
)

type modelAgentRoute struct {
	routeUserID                 string
	platform                    platform.PlatformName
	accountID                   string
	modelSettingCard            bool
	modelSettingCodexThreadID   string
	modelSettingClaudeSessionID string
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
	if !h.modelSettingCardAgentMatches(ctx, req) {
		sendPlatformText(ctx, req.Reply, msg.UserID, expiredModelSettingCardText())
		return true
	}
	setting, prefix := modelSettingModel, "/model"
	if strings.HasPrefix(req.Trimmed, "/reasoning") {
		setting, prefix = modelSettingReasoning, "/reasoning"
	}
	arg := strings.TrimSpace(strings.TrimPrefix(req.Trimmed, prefix))
	route := modelAgentRoute{routeUserID: req.RouteUserID, platform: msg.Platform, accountID: msg.AccountID}
	if command := msg.RawCommand; command != nil && command.Action == "choice" {
		route.modelSettingCard = true
		route.modelSettingCodexThreadID = strings.TrimSpace(command.Value[modelSettingThreadMetadataKey])
		route.modelSettingClaudeSessionID = strings.TrimSpace(command.Value[modelSettingClaudeSessionMetadataKey])
	}
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

func expiredModelSettingCardText() string {
	return "该模型设置卡片已失效，请重新发送 /model 或 /reasoning。"
}

// modelSettingCardAgentMatches 拒绝缺少目标或已切换 Agent 的旧模型卡片。
func (h *Handler) modelSettingCardAgentMatches(ctx context.Context, req platformCommandRequest) bool {
	command := req.Message.RawCommand
	if command == nil || command.Action != "choice" {
		return true
	}
	expected := strings.TrimSpace(command.Value[modelSettingAgentMetadataKey])
	if expected == "" {
		return false
	}
	current := h.defaultAgentNameForRoute(req.RouteUserID, req.Message.Platform, req.Message.AccountID)
	if expected != current {
		return false
	}
	ag, err := h.getAgent(ctx, current)
	if err != nil || ag == nil {
		return false
	}
	route := modelAgentRoute{
		routeUserID: req.RouteUserID,
		platform:    req.Message.Platform,
		accountID:   req.Message.AccountID,
	}
	if isCodexAgent(current, ag.Info()) {
		expectedThread := strings.TrimSpace(command.Value[modelSettingThreadMetadataKey])
		currentRef, bound := h.currentCodexSessionSettingRef(route, current)
		if expectedThread == "" {
			return !bound
		}
		return bound && currentRef.threadID == expectedThread
	}
	if isClaudeAgent(current, ag.Info()) {
		expectedSession := strings.TrimSpace(command.Value[modelSettingClaudeSessionMetadataKey])
		currentRef, bound := h.currentClaudeSessionSettingRef(route, current)
		if expectedSession == "" {
			return !bound
		}
		return bound && currentRef.sessionID == expectedSession
	}
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
	control = h.withCurrentSessionStatus(modelSettingControllerRequest{
		ctx: ctx, route: route, name: name, agent: ag, controller: control,
	})
	if arg == "" {
		return renderModelOverview(ctx, control)
	}
	if reply, handled := h.setCurrentClaudeSessionSetting(claudeModelSettingRequest{
		ctx: ctx, route: route, name: name, agent: ag, model: arg,
	}); handled {
		return reply
	}
	if reply, handled := h.setCurrentCodexSessionSetting(codexModelSettingRequest{
		ctx: ctx, route: route, name: name, agent: ag, model: arg,
	}); handled {
		return reply
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
	control = h.withCurrentSessionStatus(modelSettingControllerRequest{
		ctx: ctx, route: route, name: name, agent: ag, controller: control,
	})
	if arg == "" {
		return renderReasoningOverview(ctx, control)
	}
	if reply, handled := h.setCurrentClaudeSessionSetting(claudeModelSettingRequest{
		ctx: ctx, route: route, name: name, agent: ag, effort: arg,
	}); handled {
		return reply
	}
	if reply, handled := h.setCurrentCodexSessionSetting(codexModelSettingRequest{
		ctx: ctx, route: route, name: name, agent: ag, effort: arg,
	}); handled {
		return reply
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
