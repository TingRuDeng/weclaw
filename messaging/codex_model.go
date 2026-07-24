package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
)

// handleCodexModelCommand 处理 Codex 模型目录与新会话默认配置查询命令。
func (h *Handler) handleCodexModelCommand(ctx context.Context, ag agent.Agent, args []string) string {
	if len(args) == 0 || args[0] == "status" {
		return h.renderCodexModelStatus(ag)
	}
	if args[0] == "ls" {
		return h.renderCodexModelList(ctx, ag)
	}
	return "用法: /cx model status | /cx model ls"
}

// isCodexModelStatusArgs 判断命令是否只需要读取本地配置。
func isCodexModelStatusArgs(args []string) bool {
	return len(args) == 0 || len(args) == 1 && args[0] == "status"
}

// renderCodexModelStatusFromConfig 不启动 Codex 子进程，便于 Codex 本身损坏时仍可查看配置。
func (h *Handler) renderCodexModelStatusFromConfig() string {
	agentName, ok := h.codexAgentName()
	if !ok {
		return "当前没有配置 Codex Agent。"
	}
	status, ok := h.codexModelStatusFromMemory(agentName)
	if !ok {
		return "当前 Codex Agent 不支持模型配置查询。"
	}
	return renderCodexModelStatusText(status)
}

// renderCodexModelStatus 渲染已运行 Agent 暴露的模型配置。
func (h *Handler) renderCodexModelStatus(ag agent.Agent) string {
	modelAg, ok := ag.(agent.CodexModelAgent)
	if !ok {
		return "当前 Codex Agent 不支持模型配置查询。"
	}
	return renderCodexModelStatusText(modelAg.CodexModelStatus())
}

// codexModelStatusFromMemory 优先读取已运行 Agent，其次读取启动配置元信息。
func (h *Handler) codexModelStatusFromMemory(agentName string) (agent.CodexModelStatus, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ag, ok := h.agents[agentName]; ok {
		if modelAg, ok := ag.(agent.CodexModelAgent); ok {
			return modelAg.CodexModelStatus(), true
		}
	}
	for _, meta := range h.agentMetas {
		if meta.Name == agentName {
			return agent.CodexModelStatus{Model: meta.Model, Effort: meta.Effort}, true
		}
	}
	return agent.CodexModelStatus{}, false
}

// renderCodexModelStatusText 明确展示新建 thread 默认值，避免与 /model 当前会话语义混淆。
func renderCodexModelStatusText(status agent.CodexModelStatus) string {
	return wechatCommandText(
		"Codex 新会话默认模型配置:",
		"model: "+codexModelConfigValue(status.Model),
		"effort: "+codexModelConfigValue(status.Effort),
		"speed: "+codexServiceTierDefaultLabel(status.ServiceTier),
	)
}

// renderCodexModelList 查询并渲染 Codex app-server 返回的模型列表。
func (h *Handler) renderCodexModelList(ctx context.Context, ag agent.Agent) string {
	modelAg, ok := ag.(agent.CodexModelAgent)
	if !ok {
		return "当前 Codex Agent 不支持 model/list。"
	}
	models, err := modelAg.ListCodexModels(ctx)
	if err != nil {
		return fmt.Sprintf("查询 Codex 模型失败: %v", err)
	}
	if len(models) == 0 {
		return "Codex 当前没有返回可用模型。"
	}
	lines := []string{"Codex 可用模型:"}
	for index, model := range models {
		lines = append(lines, fmt.Sprintf("%d. %s", index, codexModelLabel(model)))
		if len(model.EffortOptions) > 0 {
			lines = append(lines, "effort: "+strings.Join(model.EffortOptions, ", "))
		}
	}
	return wechatCommandText(lines...)
}

// codexModelConfigValue 用明确文案区分空配置和真实字符串。
func codexModelConfigValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(Codex 默认)"
	}
	return value
}

// codexModelLabel 在名称不同于 ID 时补充展示名称。
func codexModelLabel(model agent.CodexModel) string {
	if strings.TrimSpace(model.Name) == "" || model.Name == model.ID {
		return model.ID
	}
	return model.ID + " (" + model.Name + ")"
}
