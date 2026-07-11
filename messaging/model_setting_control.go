package messaging

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type modelSettingStatus struct {
	Model  string
	Effort string
}

type modelSettingOption struct {
	ID            string
	Alias         string
	Label         string
	EffortOptions []string
}

type modelSettingController interface {
	AgentLabel() string
	Status() modelSettingStatus
	ListModels(context.Context) ([]modelSettingOption, error)
	SetModel(model string, effort string)
	DefaultValue() string
}

type codexModelSettingController struct {
	agent.CodexModelControlAgent
}

func (c codexModelSettingController) AgentLabel() string { return "Codex" }
func (c codexModelSettingController) Status() modelSettingStatus {
	status := c.CodexModelStatus()
	return modelSettingStatus{Model: status.Model, Effort: status.Effort}
}
func (c codexModelSettingController) ListModels(ctx context.Context) ([]modelSettingOption, error) {
	models, err := c.ListCodexModels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]modelSettingOption, 0, len(models))
	for _, model := range models {
		result = append(result, modelSettingOption{
			ID: model.ID, Label: codexModelLabel(model), EffortOptions: model.EffortOptions,
		})
	}
	return result, nil
}
func (c codexModelSettingController) SetModel(model string, effort string) {
	c.SetCodexModel(model, effort)
}
func (c codexModelSettingController) DefaultValue() string { return "(Codex 默认)" }

type claudeModelSettingController struct {
	agent.ClaudeModelControlAgent
}

func (c claudeModelSettingController) AgentLabel() string { return "Claude" }
func (c claudeModelSettingController) Status() modelSettingStatus {
	status := c.ClaudeModelStatus()
	return modelSettingStatus{Model: status.Model, Effort: status.Effort}
}
func (c claudeModelSettingController) ListModels(ctx context.Context) ([]modelSettingOption, error) {
	models, err := c.ListClaudeModels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]modelSettingOption, 0, len(models))
	for _, model := range models {
		result = append(result, modelSettingOption{
			ID: model.ID, Alias: model.Alias, Label: claudeModelLabel(model), EffortOptions: model.EffortOptions,
		})
	}
	return result, nil
}
func (c claudeModelSettingController) SetModel(model string, effort string) {
	c.SetClaudeModel(model, effort)
}
func (c claudeModelSettingController) DefaultValue() string { return "(Claude Code 默认)" }

func newModelSettingController(name string, ag agent.Agent) (modelSettingController, bool) {
	if isCodexAgent(name, ag.Info()) {
		control, ok := ag.(agent.CodexModelControlAgent)
		return codexModelSettingController{control}, ok
	}
	if isClaudeAgent(name, ag.Info()) {
		control, ok := ag.(agent.ClaudeModelControlAgent)
		return claudeModelSettingController{control}, ok
	}
	return nil, false
}

func renderModelOverview(ctx context.Context, control modelSettingController) string {
	status := control.Status()
	lines := []string{control.AgentLabel() + " 当前模型: " + modelSettingValue(status.Model, control)}
	if models, err := control.ListModels(ctx); err == nil && len(models) > 0 {
		lines = append(lines, "可用模型:")
		for _, model := range models {
			lines = append(lines, "- "+model.Label)
		}
	}
	lines = append(lines, "用 /model <模型ID> 切换。")
	return wechatCommandText(lines...)
}

func renderReasoningOverview(ctx context.Context, control modelSettingController) string {
	status := control.Status()
	lines := []string{control.AgentLabel() + " 当前推理强度: " + modelSettingValue(status.Effort, control)}
	if models, err := control.ListModels(ctx); err == nil {
		if options := modelEffortOptions(models, status.Model); len(options) > 0 {
			lines = append(lines, "可选: "+strings.Join(options, ", "))
		}
	}
	lines = append(lines, "用 /reasoning <强度> 切换。")
	return wechatCommandText(lines...)
}

func modelSettingValue(value string, control modelSettingController) string {
	if strings.TrimSpace(value) == "" {
		return control.DefaultValue()
	}
	return value
}

func modelFixedByConfigHint(name string) string {
	return wechatCommandText(
		"Agent "+name+" 的模型由配置固定，不支持聊天内运行时切换。",
		"可在 config.json 修改该 agent 的 model 后重启，或用 weclaw web 面板配置。",
	)
}

// sendFeishuModelSettingCard 使用统一选择卡片展示当前会话 Agent 的模型设置。
func (h *Handler) sendFeishuModelSettingCard(ctx context.Context, req modelSettingCardRequest) bool {
	if req.message.Platform != platform.PlatformFeishu || req.reply == nil || !req.reply.Capabilities().Buttons {
		return false
	}
	name, ag, ok := h.resolveModelAgentForRoute(ctx, req.route)
	if !ok {
		return false
	}
	control, ok := newModelSettingController(name, ag)
	if !ok {
		return false
	}
	prompt, choices := modelSettingCard(ctx, control, req.setting)
	if len(choices) == 0 {
		return false
	}
	return req.reply.AskChoices(ctx, prompt, choices) == nil
}

func modelSettingCard(ctx context.Context, control modelSettingController, setting string) (string, []platform.Choice) {
	status := control.Status()
	models, err := control.ListModels(ctx)
	if err != nil || len(models) == 0 {
		return "", nil
	}
	if setting == modelSettingReasoning {
		return reasoningSettingCard(control, status, models)
	}
	return modelSelectionCard(control, status, models)
}

func modelSelectionCard(control modelSettingController, status modelSettingStatus, models []modelSettingOption) (string, []platform.Choice) {
	choices := make([]platform.Choice, 0, len(models))
	for _, model := range models {
		label := markCurrentChoice(model.Label, modelOptionMatches(model, status.Model))
		choices = append(choices, platform.Choice{ID: "/model " + model.ID, Label: label})
	}
	prompt := control.AgentLabel() + " 当前模型: " + modelSettingValue(status.Model, control) + "\n\n请选择要使用的模型。"
	return prompt, choices
}

func reasoningSettingCard(control modelSettingController, status modelSettingStatus, models []modelSettingOption) (string, []platform.Choice) {
	options := modelEffortOptions(models, status.Model)
	choices := make([]platform.Choice, 0, len(options))
	for _, effort := range options {
		choices = append(choices, platform.Choice{
			ID: "/reasoning " + effort, Label: markCurrentChoice(effort, effort == status.Effort),
		})
	}
	prompt := control.AgentLabel() + " 当前推理强度: " + modelSettingValue(status.Effort, control) + "\n\n请选择要使用的推理强度。"
	return prompt, choices
}

func modelEffortOptions(models []modelSettingOption, model string) []string {
	if strings.TrimSpace(model) == "" {
		if len(models) > 0 {
			return models[0].EffortOptions
		}
		return nil
	}
	for _, option := range models {
		if modelOptionMatches(option, model) {
			return option.EffortOptions
		}
	}
	return nil
}

func modelOptionMatches(option modelSettingOption, model string) bool {
	return option.ID == model || strings.TrimSpace(option.Alias) != "" && option.Alias == model
}

func markCurrentChoice(label string, current bool) string {
	if current {
		return label + "（当前）"
	}
	return label
}
