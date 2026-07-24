package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func (h *Handler) handleFastCommandForRoute(ctx context.Context, route modelAgentRoute, arg string) string {
	name, ag, ok := h.resolveModelAgentForRoute(ctx, route)
	if !ok {
		return "当前没有可用的默认 Agent。"
	}
	if !isCodexAgent(name, ag.Info()) {
		return "Fast 模式仅支持 Codex，请先将当前窗口切换到 Codex 会话。"
	}
	fastAgent, ok := ag.(agent.CodexFastControlAgent)
	if !ok {
		return "当前 Codex runtime 不支持 Fast 模式。"
	}
	control, ok := newModelSettingController(name, ag)
	if !ok {
		return "当前 Codex runtime 不支持 Fast 模式。"
	}
	control = h.withCurrentSessionStatus(modelSettingControllerRequest{
		ctx: ctx, route: route, name: name, agent: ag, controller: control,
	})

	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		return renderFastOverview(ctx, control)
	}
	serviceTier, ok := parseFastCommandValue(arg)
	if !ok {
		return "用法: /fast on | /fast off"
	}
	if serviceTier == agent.CodexServiceTierFast {
		supported, err := codexFastSupported(ctx, control)
		if err != nil {
			return fmt.Sprintf("查询 Codex Fast 能力失败: %v", err)
		}
		if !supported {
			return "当前 Codex 模型或账号不支持 Fast 模式，本次设置未执行。"
		}
	}
	if reply, handled := h.setCurrentCodexSessionSetting(codexModelSettingRequest{
		ctx: ctx, route: route, name: name, agent: ag, serviceTier: &serviceTier,
	}); handled {
		return reply
	}
	fastAgent.SetCodexServiceTier(serviceTier)
	return wechatCommandText(
		"已将 Codex 新会话默认速度切换为: "+codexServiceTierLabel(serviceTier),
		"将在下一个新会话生效，发送 /new 立即开新会话使用。",
	)
}

func parseFastCommandValue(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "fast":
		return agent.CodexServiceTierFast, true
	case "off", "standard", "default":
		return agent.CodexServiceTierStandard, true
	default:
		return "", false
	}
}

func renderFastOverview(ctx context.Context, control modelSettingController) string {
	status := control.Status()
	supported, err := codexFastSupported(ctx, control)
	if err != nil {
		return fmt.Sprintf("查询 Codex Fast 能力失败: %v", err)
	}
	lines := []string{
		"Codex " + modelSettingScope(control) + "速度: " + codexServiceTierStatusLabel(status),
	}
	if supported {
		lines = append(lines, "可选: 标准, Fast", "用 /fast on 或 /fast off 切换"+modelSettingScope(control)+"速度。")
	} else {
		lines = append(lines, "当前模型或账号未提供 Fast 档位。")
	}
	return wechatCommandText(lines...)
}

func fastSettingCard(control modelSettingController, status modelSettingStatus, models []modelSettingOption) (string, []platform.Choice) {
	if modelSettingScope(control) == "当前会话" && strings.TrimSpace(status.Model) == "" {
		return "", nil
	}
	if !codexFastSupportedByModels(status.Model, models) {
		return "", nil
	}
	standardCurrent := status.ServiceTier == agent.CodexServiceTierStandard ||
		status.ServiceTierKnown && strings.TrimSpace(status.ServiceTier) == ""
	choices := []platform.Choice{
		{ID: "/fast off", Label: markCurrentChoice("标准", standardCurrent)},
		{ID: "/fast on", Label: markCurrentChoice("Fast", status.ServiceTier == agent.CodexServiceTierFast)},
	}
	prompt := "Codex " + modelSettingScope(control) + "速度: " + codexServiceTierStatusLabel(status) +
		"\n\n请选择要使用的速度。Fast 会增加用量消耗。"
	return prompt, choices
}

func codexFastSupported(ctx context.Context, control modelSettingController) (bool, error) {
	status := control.Status()
	if modelSettingScope(control) == "当前会话" && strings.TrimSpace(status.Model) == "" {
		return false, nil
	}
	models, err := control.ListModels(ctx)
	if err != nil {
		return false, err
	}
	return codexFastSupportedByModels(status.Model, models), nil
}

func codexFastSupportedByModels(model string, models []modelSettingOption) bool {
	option, ok := currentModelSettingOption(model, models)
	if !ok {
		return false
	}
	for _, tier := range option.ServiceTiers {
		if strings.EqualFold(tier.Name, "Fast") ||
			strings.EqualFold(tier.ID, agent.CodexServiceTierFast) ||
			strings.EqualFold(tier.ID, "fast") {
			return true
		}
	}
	return false
}

func currentModelSettingOption(model string, models []modelSettingOption) (modelSettingOption, bool) {
	model = strings.TrimSpace(model)
	if model != "" {
		for _, option := range models {
			if modelOptionMatches(option, model) {
				return option, true
			}
		}
		return modelSettingOption{}, false
	}
	for _, option := range models {
		if option.Default {
			return option, true
		}
	}
	if len(models) > 0 {
		return models[0], true
	}
	return modelSettingOption{}, false
}

func codexServiceTierStatusLabel(status modelSettingStatus) string {
	if status.ServiceTier == agent.CodexServiceTierFast {
		return "Fast"
	}
	if status.ServiceTierKnown || status.ServiceTier == agent.CodexServiceTierStandard {
		if tier := strings.TrimSpace(status.ServiceTier); tier != "" && tier != agent.CodexServiceTierStandard {
			return tier
		}
		return "标准"
	}
	return "(Codex 默认)"
}

func codexServiceTierLabel(serviceTier string) string {
	if serviceTier == agent.CodexServiceTierFast || strings.EqualFold(serviceTier, "fast") {
		return "Fast"
	}
	return "标准"
}

func codexServiceTierDefaultLabel(serviceTier string) string {
	if strings.TrimSpace(serviceTier) == "" {
		return "(Codex 默认)"
	}
	return codexServiceTierLabel(serviceTier)
}

func codexFastStatusLine(ctx context.Context, ag agent.Agent, threadID string) string {
	configAgent, ok := ag.(agent.CodexThreadConfigAgent)
	if !ok || strings.TrimSpace(threadID) == "" {
		return ""
	}
	config, err := configAgent.CodexThreadConfig(ctx, "", threadID)
	if err != nil || config.ServiceTier != agent.CodexServiceTierFast {
		return ""
	}
	return "速度: Fast"
}
