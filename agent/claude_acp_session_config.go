package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type claudeSessionConfigRequest struct {
	sessionID string
	configID  string
	value     string
}

type claudeSessionBootstrap struct {
	sessionID string
	options   []acpSessionConfigOption
	sequence  uint64
}

// configureClaudeSession 只在新建 session 时应用 Agent 默认配置。
func (a *ACPAgent) configureClaudeSession(ctx context.Context, bootstrap claudeSessionBootstrap) error {
	a.claudeConfigMu.Lock()
	defer a.claudeConfigMu.Unlock()
	status := a.ClaudeModelStatus()
	if len(bootstrap.options) > 0 {
		if err := a.cacheClaudeSessionConfigAt(bootstrap.sessionID, bootstrap.options, bootstrap.sequence); err != nil {
			return err
		}
	}
	if status.Model == "" && status.Effort == "" {
		return nil
	}
	if len(bootstrap.options) == 0 {
		return fmt.Errorf("Claude ACP session/new 未返回 configOptions")
	}
	if status.Model != "" {
		request := claudeSessionConfigRequest{sessionID: bootstrap.sessionID, configID: claudeModelConfigID, value: status.Model}
		if err := a.setClaudeConfigValue(ctx, request); err != nil {
			return fmt.Errorf("设置 claude model 失败: %w", err)
		}
	}
	if status.Effort == "" {
		return nil
	}
	if err := a.setClaudeEffort(ctx, bootstrap.sessionID, status.Effort); err != nil {
		return fmt.Errorf("设置 claude effort 失败: %w", err)
	}
	return nil
}

// ClaudeSessionConfig 返回 conversation 当前绑定 session 的 ACP 配置快照。
func (a *ACPAgent) ClaudeSessionConfig(conversationID string) (ClaudeSessionConfig, bool) {
	if !a.isClaudeACP() {
		return ClaudeSessionConfig{}, false
	}
	a.mu.Lock()
	sessionID := a.sessions[conversationID]
	options := cloneACPConfigOptions(a.claudeSessionConfigs[sessionID])
	a.mu.Unlock()
	return claudeConfigFromOptions(options)
}

// SetClaudeSessionConfig 原子定位当前绑定，只更新该 ACP session。
func (a *ACPAgent) SetClaudeSessionConfig(ctx context.Context, update ClaudeSessionConfigUpdate) error {
	a.claudeConfigMu.Lock()
	defer a.claudeConfigMu.Unlock()
	if !a.isClaudeACP() {
		return fmt.Errorf("当前 Agent 不是 Claude ACP")
	}
	sessionID, revision, err := a.currentClaudeConfigTarget(update.ConversationID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(update.Model) == "" && strings.TrimSpace(update.Effort) == "" {
		return fmt.Errorf("模型和推理强度不能同时为空")
	}
	if model := strings.TrimSpace(update.Model); model != "" {
		request := claudeSessionConfigRequest{sessionID: sessionID, configID: claudeModelConfigID, value: model}
		if err := a.setClaudeConfigValue(ctx, request); err != nil {
			return fmt.Errorf("设置 claude model 失败: %w", err)
		}
		if strings.TrimSpace(update.Effort) != "" {
			if err := a.ensureClaudeConfigTarget(update.ConversationID, sessionID, revision); err != nil {
				return partialClaudeConfigError(model, err)
			}
		}
	}
	if effort := strings.TrimSpace(update.Effort); effort != "" {
		if err := a.setClaudeEffort(ctx, sessionID, effort); err != nil {
			return partialClaudeConfigError(strings.TrimSpace(update.Model), err)
		}
	}
	return a.ensureClaudeConfigTarget(update.ConversationID, sessionID, revision)
}

func partialClaudeConfigError(updatedModel string, cause error) error {
	if updatedModel == "" {
		return fmt.Errorf("设置 claude effort 失败: %w", cause)
	}
	return fmt.Errorf("Claude session 配置部分完成：模型已更新为 %s，但推理强度更新失败: %w", updatedModel, cause)
}

func (a *ACPAgent) currentClaudeConfigTarget(conversationID string) (string, uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessionID := strings.TrimSpace(a.sessions[conversationID])
	if sessionID == "" {
		return "", 0, fmt.Errorf("session error: %w", ErrAgentSessionNotBound)
	}
	return sessionID, a.bindingRevisions[conversationID], nil
}

func (a *ACPAgent) ensureClaudeConfigTarget(conversationID string, sessionID string, revision uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[conversationID] != sessionID || a.bindingRevisions[conversationID] != revision {
		return fmt.Errorf("Claude 会话绑定已变化，配置结果仅保留在原 session")
	}
	return nil
}

func (a *ACPAgent) setClaudeEffort(ctx context.Context, sessionID string, effort string) error {
	options := a.claudeSessionConfigSnapshot(sessionID)
	_, effortOption := findClaudeConfigOptions(options)
	if effortOption == nil {
		return fmt.Errorf("当前 session 不支持推理强度配置")
	}
	model := currentClaudeConfigModel(options)
	if !configOptionHasValue(effortOption, effort) {
		return fmt.Errorf("claude 模型 %s 不支持推理强度 %s", model, effort)
	}
	request := claudeSessionConfigRequest{sessionID: sessionID, configID: effortOption.ID, value: effort}
	return a.setClaudeConfigValue(ctx, request)
}

func (a *ACPAgent) setClaudeConfigValue(ctx context.Context, request claudeSessionConfigRequest) error {
	options := a.claudeSessionConfigSnapshot(request.sessionID)
	option := findClaudeConfigOption(options, request.configID)
	if option == nil || !configOptionHasValue(option, request.value) {
		return fmt.Errorf("配置 %s 不支持值 %s", request.configID, request.value)
	}
	request.configID = option.ID
	return a.setClaudeSessionConfig(ctx, request)
}

func (a *ACPAgent) setClaudeSessionConfig(ctx context.Context, request claudeSessionConfigRequest) error {
	result, sequence, err := a.rpcWithSequence(ctx, "session/set_config_option", sessionConfigOptionParams{
		SessionID: request.sessionID, ConfigID: request.configID, Value: request.value,
	})
	if err != nil {
		return err
	}
	options, err := requireClaudeConfigOptions(result, "session/set_config_option")
	if err != nil {
		return err
	}
	return a.cacheClaudeSessionConfigAt(request.sessionID, options, sequence)
}

func requireClaudeConfigOptions(result json.RawMessage, method string) ([]acpSessionConfigOption, error) {
	var envelope struct {
		ConfigOptions json.RawMessage `json:"configOptions"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return nil, fmt.Errorf("解析 %s 响应失败: %w", method, err)
	}
	if len(envelope.ConfigOptions) == 0 || string(envelope.ConfigOptions) == "null" {
		return nil, fmt.Errorf("%s 响应缺少完整 configOptions", method)
	}
	var options []acpSessionConfigOption
	if err := json.Unmarshal(envelope.ConfigOptions, &options); err != nil || options == nil {
		return nil, fmt.Errorf("%s 响应的完整 configOptions 无效", method)
	}
	return options, nil
}

func (a *ACPAgent) cacheClaudeResumeConfig(sessionID string, result json.RawMessage, sequence uint64) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(result, &envelope); err != nil || envelope == nil {
		return fmt.Errorf("session/resume result 必须是非 null JSON object")
	}
	_, exists := envelope["configOptions"]
	if !exists {
		return fmt.Errorf("session/resume 响应缺少完整 configOptions")
	}
	options, err := requireClaudeConfigOptions(result, "session/resume")
	if err != nil {
		return err
	}
	return a.cacheClaudeSessionConfigAt(sessionID, options, sequence)
}

func (a *ACPAgent) cacheClaudeSessionConfig(sessionID string, options []acpSessionConfigOption) error {
	return a.cacheClaudeSessionConfigAt(sessionID, options, 0)
}

func (a *ACPAgent) cacheClaudeSessionConfigAt(sessionID string, options []acpSessionConfigOption, sequence uint64) error {
	if strings.TrimSpace(sessionID) == "" || options == nil {
		return fmt.Errorf("Claude session 配置快照无效")
	}
	if err := validateClaudeConfigOptions(options); err != nil {
		return err
	}
	if _, ok := claudeConfigFromOptions(options); !ok {
		return fmt.Errorf("Claude session 配置缺少模型选项")
	}
	a.mu.Lock()
	if sequence > 0 && a.claudeConfigRevisions[sessionID] > sequence {
		a.mu.Unlock()
		return nil
	}
	a.claudeSessionConfigs[sessionID] = cloneACPConfigOptions(options)
	if sequence > 0 {
		a.claudeConfigRevisions[sessionID] = sequence
	}
	a.mu.Unlock()
	a.cacheClaudeConfigOptions(options)
	return nil
}

// validateClaudeConfigOptions 在写缓存前校验完整配置状态的唯一性和当前值。
func validateClaudeConfigOptions(options []acpSessionConfigOption) error {
	seen := make(map[string]struct{}, len(options))
	for index := range options {
		optionID := strings.TrimSpace(options[index].ID)
		if optionID == "" {
			return fmt.Errorf("Claude session configOptions[%d].id 不能为空", index)
		}
		if _, exists := seen[optionID]; exists {
			return fmt.Errorf("Claude session configOptions 包含重复 id %q", optionID)
		}
		seen[optionID] = struct{}{}
	}
	modelOption, effortOption := findClaudeConfigOptions(options)
	if err := validateCurrentConfigChoice(modelOption, "模型"); err != nil {
		return err
	}
	return validateCurrentConfigChoice(effortOption, "推理强度")
}

func validateCurrentConfigChoice(option *acpSessionConfigOption, label string) error {
	if option == nil {
		return nil
	}
	current := configStringValue(option.CurrentValue)
	if current == "" {
		return fmt.Errorf("Claude session %s当前值不能为空", label)
	}
	return nil
}

func (a *ACPAgent) claudeSessionConfigSnapshot(sessionID string) []acpSessionConfigOption {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneACPConfigOptions(a.claudeSessionConfigs[sessionID])
}

func claudeConfigFromOptions(options []acpSessionConfigOption) (ClaudeSessionConfig, bool) {
	modelOption, effortOption := findClaudeConfigOptions(options)
	if modelOption == nil {
		return ClaudeSessionConfig{}, false
	}
	config := ClaudeSessionConfig{Model: configStringValue(modelOption.CurrentValue)}
	if effortOption != nil {
		config.Effort = configStringValue(effortOption.CurrentValue)
	}
	return config, config.Model != ""
}

func cloneACPConfigOptions(source []acpSessionConfigOption) []acpSessionConfigOption {
	options := make([]acpSessionConfigOption, len(source))
	copy(options, source)
	for index := range options {
		options[index].Options = append([]acpSessionConfigChoice(nil), source[index].Options...)
	}
	return options
}

func configStringValue(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func configOptionHasValue(option *acpSessionConfigOption, value string) bool {
	for _, choice := range option.Options {
		if choice.Value == value {
			return true
		}
	}
	return false
}
