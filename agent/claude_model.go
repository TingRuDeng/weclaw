package agent

import (
	"context"
	"fmt"
	"strings"
)

var defaultClaudeEffortOptions = []string{"low", "medium", "high", "xhigh", "max"}

var defaultClaudeModels = []ClaudeModel{
	{
		ID:            "claude-fable-5",
		Name:          "Claude Fable 5",
		Alias:         "fable",
		Description:   "长任务和高能力代理场景",
		EffortOptions: defaultClaudeEffortOptions,
	},
	{
		ID:            "claude-opus-4-8",
		Name:          "Claude Opus 4.8",
		Alias:         "opus",
		Description:   "复杂编码和企业工作",
		EffortOptions: defaultClaudeEffortOptions,
	},
	{
		ID:            "claude-sonnet-5",
		Name:          "Claude Sonnet 5",
		Alias:         "sonnet",
		Description:   "速度和能力均衡",
		EffortOptions: defaultClaudeEffortOptions,
	},
	{
		ID:            "claude-haiku-4-5",
		Name:          "Claude Haiku 4.5",
		Alias:         "haiku",
		Description:   "低延迟和轻量任务",
		EffortOptions: defaultClaudeEffortOptions,
	},
}

// DefaultClaudeModels 返回 Claude Code 官方文档中的常用模型清单副本。
func DefaultClaudeModels() []ClaudeModel {
	models := make([]ClaudeModel, len(defaultClaudeModels))
	for index, model := range defaultClaudeModels {
		models[index] = model
		models[index].EffortOptions = append([]string(nil), model.EffortOptions...)
	}
	return models
}

// ClaudeModelStatus 返回当前 CLI Agent 配置的 Claude Code 模型。
func (a *CLIAgent) ClaudeModelStatus() ClaudeModelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ClaudeModelStatus{Model: a.model, Effort: a.effort}
}

// SetClaudeModel 更新后续新建 Claude session 使用的模型配置。
func (a *CLIAgent) SetClaudeModel(model string, effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if value := strings.TrimSpace(model); value != "" {
		a.model = value
	}
	if value := strings.TrimSpace(effort); value != "" {
		a.effort = value
	}
}

// ListClaudeModels 返回无需启动 Claude Code 交互界面的内置模型清单。
func (a *CLIAgent) ListClaudeModels(_ context.Context) ([]ClaudeModel, error) {
	if !a.isClaudeCLI() {
		return nil, fmt.Errorf("当前 Agent 不是 Claude CLI")
	}
	return DefaultClaudeModels(), nil
}
