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
	text := h.handleModelCommandForRoute(ctx, route, arg)
	if setting == modelSettingReasoning {
		text = h.handleReasoningCommandForRoute(ctx, route, arg)
	}
	sendPlatformText(ctx, req.Reply, msg.UserID, text)
	return true
}

// handleModelCommand 统一的 /model 入口：查看/切换默认 agent 的模型。
// Codex(ACP) 支持运行时切换(下个新会话生效)；其它 agent 模型由配置固定。
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
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return h.renderModelOverview(ctx, ag)
	}
	control, ok := ag.(agent.CodexModelControlAgent)
	if !ok {
		return modelFixedByConfigHint(name)
	}
	control.SetCodexModel(arg, "")
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
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return h.renderReasoningOverview(ctx, ag)
	}
	control, ok := ag.(agent.CodexModelControlAgent)
	if !ok {
		return modelFixedByConfigHint(name)
	}
	control.SetCodexModel("", arg)
	return wechatCommandText(
		"已将 "+name+" 推理强度切换为: "+arg,
		"将在下一个新会话生效，发送 /new 立即开新会话使用。",
	)
}

// resolveModelAgent 解析当前平台的默认 agent 并确保其已启动。
func (h *Handler) resolveModelAgent(ctx context.Context, platformName platform.PlatformName) (string, agent.Agent, bool) {
	return h.resolveModelAgentForAccount(ctx, platformName, "")
}

func (h *Handler) resolveModelAgentForAccount(ctx context.Context, platformName platform.PlatformName, accountID string) (string, agent.Agent, bool) {
	return h.resolveModelAgentForRoute(ctx, modelAgentRoute{platform: platformName, accountID: accountID})
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

func (h *Handler) renderModelOverview(ctx context.Context, ag agent.Agent) string {
	modelAg, ok := ag.(agent.CodexModelAgent)
	if !ok {
		return modelFixedByConfigHint(ag.Info().Name)
	}
	lines := []string{"当前模型: " + codexModelConfigValue(modelAg.CodexModelStatus().Model)}
	if models, err := modelAg.ListCodexModels(ctx); err == nil && len(models) > 0 {
		lines = append(lines, "可用模型:")
		for _, model := range models {
			lines = append(lines, "- "+codexModelLabel(model))
		}
		lines = append(lines, "用 /model <模型ID> 切换。")
	} else {
		lines = append(lines, "用 /model <模型ID> 切换。")
	}
	return wechatCommandText(lines...)
}

func (h *Handler) renderReasoningOverview(ctx context.Context, ag agent.Agent) string {
	modelAg, ok := ag.(agent.CodexModelAgent)
	if !ok {
		return modelFixedByConfigHint(ag.Info().Name)
	}
	status := modelAg.CodexModelStatus()
	lines := []string{"当前推理强度: " + codexModelConfigValue(status.Effort)}
	if options := currentModelEffortOptions(ctx, modelAg, status.Model); len(options) > 0 {
		lines = append(lines, "可选: "+strings.Join(options, ", "))
	}
	lines = append(lines, "用 /reasoning <强度> 切换。")
	return wechatCommandText(lines...)
}

// currentModelEffortOptions 返回当前模型支持的推理强度选项(若可查询)。
func currentModelEffortOptions(ctx context.Context, modelAg agent.CodexModelAgent, model string) []string {
	models, err := modelAg.ListCodexModels(ctx)
	if err != nil {
		return nil
	}
	return modelEffortOptions(models, model)
}

// modelEffortOptions 只返回当前模型声明支持的推理强度。
func modelEffortOptions(models []agent.CodexModel, model string) []string {
	if strings.TrimSpace(model) == "" {
		if len(models) > 0 {
			return models[0].EffortOptions
		}
		return nil
	}
	for _, m := range models {
		if m.ID == model {
			return m.EffortOptions
		}
	}
	return nil
}

// sendFeishuModelSettingCard 使用统一选择卡片展示 Codex 模型设置。
func (h *Handler) sendFeishuModelSettingCard(ctx context.Context, req modelSettingCardRequest) bool {
	msg := req.message
	reply := req.reply
	if msg.Platform != platform.PlatformFeishu || reply == nil || !reply.Capabilities().Buttons {
		return false
	}
	_, ag, ok := h.resolveModelAgentForRoute(ctx, req.route)
	if !ok {
		return false
	}
	modelAg, ok := ag.(agent.CodexModelAgent)
	if !ok {
		return false
	}
	prompt, choices := modelSettingCard(ctx, modelAg, req.setting)
	if len(choices) == 0 {
		return false
	}
	return reply.AskChoices(ctx, prompt, choices) == nil
}

// modelSettingCard 根据实时模型目录构造卡片文案和可执行命令。
func modelSettingCard(ctx context.Context, modelAg agent.CodexModelAgent, setting string) (string, []platform.Choice) {
	status := modelAg.CodexModelStatus()
	models, err := modelAg.ListCodexModels(ctx)
	if err != nil || len(models) == 0 {
		return "", nil
	}
	if setting == modelSettingReasoning {
		return reasoningSettingCard(status, models)
	}
	return modelSelectionCard(status, models)
}

func modelSelectionCard(status agent.CodexModelStatus, models []agent.CodexModel) (string, []platform.Choice) {
	choices := make([]platform.Choice, 0, len(models))
	for _, model := range models {
		label := markCurrentChoice(codexModelLabel(model), model.ID == status.Model)
		choices = append(choices, platform.Choice{ID: "/model " + model.ID, Label: label})
	}
	prompt := "当前模型: " + codexModelConfigValue(status.Model) + "\n\n请选择要使用的模型。"
	return prompt, choices
}

func reasoningSettingCard(status agent.CodexModelStatus, models []agent.CodexModel) (string, []platform.Choice) {
	options := modelEffortOptions(models, status.Model)
	choices := make([]platform.Choice, 0, len(options))
	for _, effort := range options {
		label := markCurrentChoice(effort, effort == status.Effort)
		choices = append(choices, platform.Choice{ID: "/reasoning " + effort, Label: label})
	}
	prompt := "当前推理强度: " + codexModelConfigValue(status.Effort) + "\n\n请选择要使用的推理强度。"
	return prompt, choices
}

func markCurrentChoice(label string, current bool) string {
	if current {
		return label + "（当前）"
	}
	return label
}

func modelFixedByConfigHint(name string) string {
	return wechatCommandText(
		"Agent "+name+" 的模型由配置固定，不支持聊天内运行时切换。",
		"可在 config.json 修改该 agent 的 model 后重启，或用 weclaw web 面板配置。",
	)
}
