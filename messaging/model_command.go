package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// handleModelCommand 统一的 /model 入口：查看/切换默认 agent 的模型。
// Codex(ACP) 支持运行时切换(下个新会话生效)；其它 agent 模型由配置固定。
func (h *Handler) handleModelCommand(ctx context.Context, platformName platform.PlatformName, arg string) string {
	name, ag, ok := h.resolveModelAgent(ctx, platformName)
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
	name, ag, ok := h.resolveModelAgent(ctx, platformName)
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
	name := h.defaultAgentNameForPlatform(platformName)
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
	for _, m := range models {
		if m.ID == model {
			return m.EffortOptions
		}
	}
	// 模型未在列表中(如沿用默认)时，返回首个模型的选项作为参考
	if len(models) > 0 {
		return models[0].EffortOptions
	}
	return nil
}

func modelFixedByConfigHint(name string) string {
	return wechatCommandText(
		"Agent "+name+" 的模型由配置固定，不支持聊天内运行时切换。",
		"可在 config.json 修改该 agent 的 model 后重启，或用 weclaw web 面板配置。",
	)
}
