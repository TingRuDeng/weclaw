package agent

import (
	"context"
	"fmt"
)

var defaultClaudeModels = []ClaudeModel{
	{
		ID:          "claude-fable-5",
		Name:        "Claude Fable 5",
		Alias:       "fable",
		Description: "长任务和高能力代理场景",
	},
	{
		ID:          "claude-opus-4-8",
		Name:        "Claude Opus 4.8",
		Alias:       "opus",
		Description: "复杂编码和企业工作",
	},
	{
		ID:          "claude-sonnet-5",
		Name:        "Claude Sonnet 5",
		Alias:       "sonnet",
		Description: "速度和能力均衡",
	},
	{
		ID:          "claude-haiku-4-5",
		Name:        "Claude Haiku 4.5",
		Alias:       "haiku",
		Description: "低延迟和轻量任务",
	},
}

// DefaultClaudeModels 返回 Claude Code 官方文档中的常用模型清单副本。
func DefaultClaudeModels() []ClaudeModel {
	models := make([]ClaudeModel, len(defaultClaudeModels))
	copy(models, defaultClaudeModels)
	return models
}

// ClaudeModelStatus 返回当前 CLI Agent 配置的 Claude Code 模型。
func (a *CLIAgent) ClaudeModelStatus() ClaudeModelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ClaudeModelStatus{Model: a.model}
}

// ListClaudeModels 返回无需启动 Claude Code 交互界面的内置模型清单。
func (a *CLIAgent) ListClaudeModels(_ context.Context) ([]ClaudeModel, error) {
	if !a.isClaudeCLI() {
		return nil, fmt.Errorf("当前 Agent 不是 Claude CLI")
	}
	return DefaultClaudeModels(), nil
}
