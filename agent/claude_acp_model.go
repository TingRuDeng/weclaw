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
	modelValue := strings.TrimSpace(model)
	effortValue := strings.TrimSpace(effort)
	if modelValue != "" {
		modelChanged := modelValue != a.model
		a.model = modelValue
		if modelChanged && effortValue == "" {
			a.effort = ""
		}
	}
	if effortValue != "" {
		a.effort = effortValue
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
		return defaultClaudeACPModels(), nil
	}
	return models, nil
}

func (a *ACPAgent) configureClaudeSession(ctx context.Context, sessionID string, options []acpSessionConfigOption) error {
	a.cacheClaudeConfigOptions(options)
	status := a.ClaudeModelStatus()
	selectedModel := firstNonEmptyString(status.Model, currentClaudeConfigModel(options))
	if status.Model != "" {
		request := claudeSessionConfigRequest{sessionID: sessionID, configID: claudeModelConfigID, value: status.Model}
		if err := a.setClaudeSessionConfig(ctx, request); err != nil {
			return fmt.Errorf("设置 claude model 失败: %w", err)
		}
	}
	if status.Effort == "" {
		return nil
	}
	if !a.claudeEffortSupported(selectedModel, status.Effort) {
		return fmt.Errorf("claude 模型 %s 不支持推理强度 %s", selectedModel, status.Effort)
	}
	request := claudeSessionConfigRequest{sessionID: sessionID, configID: claudeEffortConfigID, value: status.Effort}
	if err := a.setClaudeSessionConfig(ctx, request); err != nil {
		return fmt.Errorf("设置 claude effort 失败: %w", err)
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
	a.mu.Lock()
	previous := cloneClaudeModels(a.claudeModels)
	a.mu.Unlock()
	currentModel := strings.TrimSpace(modelOption.CurrentValue)
	currentEfforts := configChoiceValues(effortOption)
	models := make([]ClaudeModel, 0, len(modelOption.Options))
	for _, option := range modelOption.Options {
		if strings.TrimSpace(option.Value) == "" {
			continue
		}
		efforts := cachedClaudeEfforts(previous, option.Value)
		if option.Value == currentModel {
			efforts = currentEfforts
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

func defaultClaudeACPModels() []ClaudeModel {
	models := DefaultClaudeModels()
	for index := range models {
		models[index].EffortOptions = nil
	}
	return models
}

func cachedClaudeEfforts(models []ClaudeModel, model string) []string {
	for _, candidate := range models {
		if candidate.Alias == model || candidate.ID == model {
			return append([]string(nil), candidate.EffortOptions...)
		}
	}
	return nil
}

func currentClaudeConfigModel(options []acpSessionConfigOption) string {
	modelOption, _ := findClaudeConfigOptions(options)
	if modelOption == nil {
		return ""
	}
	return strings.TrimSpace(modelOption.CurrentValue)
}

func (a *ACPAgent) claudeEffortSupported(model string, effort string) bool {
	a.mu.Lock()
	models := cloneClaudeModels(a.claudeModels)
	a.mu.Unlock()
	for _, candidate := range models {
		if candidate.Alias != model && candidate.ID != model {
			continue
		}
		for _, option := range candidate.EffortOptions {
			if option == effort {
				return true
			}
		}
	}
	return false
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
