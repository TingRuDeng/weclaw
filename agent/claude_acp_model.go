package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	claudeModelConfigID  = "model"
	claudeEffortConfigID = "effort"
)

type claudeSessionConfigRequest struct {
	sessionID string
	configID  string
	value     string
}

func (a *ACPAgent) isClaudeLegacyACP() bool {
	if a.protocol != protocolLegacyACP {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(a.command)), "claude")
}

// ClaudeModelStatus 返回后续新建 Claude ACP session 使用的运行时配置。
func (a *ACPAgent) ClaudeModelStatus() ClaudeModelStatus {
	config := a.modelConfigSnapshot()
	return ClaudeModelStatus{Model: config.model, Effort: config.effort}
}

// SetClaudeModel 更新后续新建 Claude ACP session 使用的模型配置。
func (a *ACPAgent) SetClaudeModel(model string, effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if value := strings.TrimSpace(model); value != "" {
		a.model = value
	}
	if value := strings.TrimSpace(effort); value != "" {
		a.effort = value
	}
}

// ListClaudeModels 优先返回 Claude ACP 最近一次 session 暴露的模型目录。
func (a *ACPAgent) ListClaudeModels(_ context.Context) ([]ClaudeModel, error) {
	if !a.isClaudeLegacyACP() {
		return nil, fmt.Errorf("当前 Agent 不是 Claude ACP")
	}
	a.mu.Lock()
	models := cloneClaudeModels(a.claudeModels)
	a.mu.Unlock()
	if len(models) == 0 {
		return DefaultClaudeModels(), nil
	}
	return models, nil
}

func (a *ACPAgent) configureClaudeSession(ctx context.Context, sessionID string, options []acpSessionConfigOption) error {
	a.cacheClaudeConfigOptions(options)
	status := a.ClaudeModelStatus()
	settings := []struct {
		id    string
		value string
	}{{claudeModelConfigID, status.Model}, {claudeEffortConfigID, status.Effort}}
	for _, setting := range settings {
		if strings.TrimSpace(setting.value) == "" {
			continue
		}
		request := claudeSessionConfigRequest{sessionID: sessionID, configID: setting.id, value: setting.value}
		if err := a.setClaudeSessionConfig(ctx, request); err != nil {
			return fmt.Errorf("设置 Claude %s 失败: %w", setting.id, err)
		}
	}
	return nil
}

func (a *ACPAgent) setClaudeSessionConfig(ctx context.Context, request claudeSessionConfigRequest) error {
	result, err := a.rpc(ctx, "session/set_config_option", sessionConfigOptionParams{
		SessionID: request.sessionID,
		ConfigID:  request.configID,
		Value:     request.value,
	})
	if err != nil {
		return err
	}
	var response sessionConfigOptionResult
	if len(result) > 0 && json.Unmarshal(result, &response) == nil {
		a.cacheClaudeConfigOptions(response.ConfigOptions)
	}
	return nil
}

func (a *ACPAgent) cacheClaudeConfigOptions(options []acpSessionConfigOption) {
	modelOption, effortOption := findClaudeConfigOptions(options)
	if modelOption == nil || len(modelOption.Options) == 0 {
		return
	}
	efforts := configChoiceValues(effortOption)
	models := make([]ClaudeModel, 0, len(modelOption.Options))
	for _, option := range modelOption.Options {
		if strings.TrimSpace(option.Value) == "" {
			continue
		}
		models = append(models, claudeModelFromConfigChoice(option, efforts))
	}
	if len(models) == 0 {
		return
	}
	a.mu.Lock()
	a.claudeModels = models
	a.mu.Unlock()
}

func claudeModelFromConfigChoice(option acpSessionConfigChoice, efforts []string) ClaudeModel {
	model := ClaudeModel{
		ID: option.Value, Name: option.Name, Alias: option.Value,
		Description: option.Description, EffortOptions: append([]string(nil), efforts...),
	}
	for _, known := range DefaultClaudeModels() {
		if option.Value == known.Alias || option.Value == known.ID {
			model.ID = known.ID
			if strings.TrimSpace(model.Name) == "" {
				model.Name = known.Name
			}
			break
		}
	}
	return model
}

func findClaudeConfigOptions(options []acpSessionConfigOption) (*acpSessionConfigOption, *acpSessionConfigOption) {
	var modelOption, effortOption *acpSessionConfigOption
	for index := range options {
		switch options[index].ID {
		case claudeModelConfigID:
			modelOption = &options[index]
		case claudeEffortConfigID:
			effortOption = &options[index]
		}
	}
	return modelOption, effortOption
}

func configChoiceValues(option *acpSessionConfigOption) []string {
	if option == nil {
		return nil
	}
	values := make([]string, 0, len(option.Options))
	for _, choice := range option.Options {
		if value := strings.TrimSpace(choice.Value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func cloneClaudeModels(source []ClaudeModel) []ClaudeModel {
	models := make([]ClaudeModel, len(source))
	for index, model := range source {
		models[index] = model
		models[index].EffortOptions = append([]string(nil), model.EffortOptions...)
	}
	return models
}
