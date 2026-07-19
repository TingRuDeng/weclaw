package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// handleClaudeModelCommand 处理 Claude 模型目录与新会话默认配置查询命令。
func (h *Handler) handleClaudeModelCommand(ctx context.Context, ag agent.Agent, args []string) string {
	if len(args) == 0 || args[0] == "status" {
		return h.renderClaudeModelStatus(ag)
	}
	if args[0] == "ls" {
		return h.renderClaudeModelList(ctx, ag)
	}
	return "用法: /cc model status | /cc model ls"
}

// renderClaudeModelStatus 渲染后续新建 Claude session 使用的默认配置。
func (h *Handler) renderClaudeModelStatus(ag agent.Agent) string {
	if modelAg, ok := ag.(agent.ClaudeModelAgent); ok {
		return renderClaudeModelStatusText(modelAg.ClaudeModelStatus())
	}
	return renderClaudeModelStatusText(agent.ClaudeModelStatus{Model: ag.Info().Model})
}

// renderClaudeModelList 渲染 Claude Code 可选模型清单。
func (h *Handler) renderClaudeModelList(ctx context.Context, ag agent.Agent) string {
	models, err := h.claudeModelsForAgent(ctx, ag)
	if err != nil {
		log.Printf("[claude-model] 查询模型列表失败: %v", err)
		return "查询 Claude 模型失败，请稍后重试。"
	}
	if len(models) == 0 {
		return "Claude 当前没有可展示的模型。"
	}
	lines := []string{"Claude 可用模型:"}
	for index, model := range models {
		lines = append(lines, fmt.Sprintf("%d. %s", index, claudeModelLabel(model)))
		if strings.TrimSpace(model.Alias) != "" {
			lines = append(lines, "alias: "+model.Alias)
		}
		if strings.TrimSpace(model.Description) != "" {
			lines = append(lines, "说明: "+model.Description)
		}
	}
	lines = append(lines, "来源: Claude Code 官方常用模型清单；实际可用性仍受账号、组织策略和 provider 限制。")
	return wechatCommandText(lines...)
}

// claudeModelsForAgent 优先读取 Agent 暴露的模型清单，否则使用内置清单。
func (h *Handler) claudeModelsForAgent(ctx context.Context, ag agent.Agent) ([]agent.ClaudeModel, error) {
	if modelAg, ok := ag.(agent.ClaudeModelAgent); ok {
		return modelAg.ListClaudeModels(ctx)
	}
	return agent.DefaultClaudeModels(), nil
}

// renderClaudeModelStatusText 用明确文案和完整字段区分新会话默认值与当前 session 配置。
func renderClaudeModelStatusText(status agent.ClaudeModelStatus) string {
	return wechatCommandText(
		"Claude 新会话默认模型配置:",
		"model: "+claudeModelConfigValue(status.Model),
		"effort: "+claudeModelConfigValue(status.Effort),
	)
}

// claudeModelConfigValue 把空配置展示为 Claude Code 默认语义。
func claudeModelConfigValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(Claude Code 默认)"
	}
	return value
}

// claudeModelLabel 在名称不同于 ID 时补充展示名称。
func claudeModelLabel(model agent.ClaudeModel) string {
	if strings.TrimSpace(model.Name) == "" || model.Name == model.ID {
		return model.ID
	}
	return model.ID + " (" + model.Name + ")"
}
