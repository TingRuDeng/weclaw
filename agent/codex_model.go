package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CodexModelStatus 返回新建 Codex thread 的默认配置，空值表示沿用 Codex 默认值。
func (a *ACPAgent) CodexModelStatus() CodexModelStatus {
	config := a.modelConfigSnapshot()
	return CodexModelStatus{Model: config.model, Effort: config.effort, ServiceTier: config.serviceTier}
}

// SetCodexModel 更新新建 Codex thread 的默认模型/推理强度；空字符串表示保持原值。
// 已存在的 thread 必须使用 SetCodexThreadConfig，禁止用共享默认值覆盖其他窗口。
func (a *ACPAgent) SetCodexModel(model string, effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(model) != "" {
		a.model = strings.TrimSpace(model)
	}
	if strings.TrimSpace(effort) != "" {
		a.effort = strings.TrimSpace(effort)
	}
}

// SetCodexServiceTier 更新新建 Codex thread 的默认速度档位。
func (a *ACPAgent) SetCodexServiceTier(serviceTier string) {
	serviceTier = normalizeCodexServiceTier(serviceTier)
	if serviceTier != CodexServiceTierFast && serviceTier != CodexServiceTierStandard {
		return
	}
	a.mu.Lock()
	a.serviceTier = serviceTier
	a.mu.Unlock()
}

// CodexThreadConfig 返回 app-server 生命周期响应或设置通知中的 thread 配置。
func (a *ACPAgent) CodexThreadConfig(_ context.Context, _ string, threadID string) (CodexThreadConfig, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return CodexThreadConfig{}, fmt.Errorf("Codex thread ID 为空")
	}
	a.mu.Lock()
	config, ok := a.codexThreadConfigs[threadID]
	a.mu.Unlock()
	if !ok {
		return CodexThreadConfig{}, fmt.Errorf("Codex thread 配置尚未加载")
	}
	return config, nil
}

// SetCodexThreadConfig 通过官方 thread/settings/update 更新当前 thread 后续 turn 的配置。
func (a *ACPAgent) SetCodexThreadConfig(ctx context.Context, update CodexThreadConfigUpdate) error {
	if a.protocol != protocolCodexAppServer {
		return fmt.Errorf("当前 Agent 不支持 Codex thread 配置")
	}
	threadID := strings.TrimSpace(update.ThreadID)
	model := strings.TrimSpace(update.Model)
	effort := strings.TrimSpace(update.Effort)
	serviceTier := ""
	serviceTierSet := update.ServiceTier != nil
	if serviceTierSet {
		serviceTier = normalizeCodexServiceTier(*update.ServiceTier)
		if serviceTier != CodexServiceTierFast && serviceTier != CodexServiceTierStandard {
			return fmt.Errorf("Codex service tier 无效")
		}
	}
	if threadID == "" {
		return fmt.Errorf("Codex thread ID 为空")
	}
	if model == "" && effort == "" && !serviceTierSet {
		return fmt.Errorf("Codex thread 配置没有可更新字段")
	}
	params := map[string]interface{}{"threadId": threadID}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	if serviceTierSet {
		if serviceTier == CodexServiceTierStandard {
			params["serviceTier"] = nil
		} else {
			params["serviceTier"] = serviceTier
		}
	}
	if _, sequence, err := a.rpcWithSequence(ctx, "thread/settings/update", params); err != nil {
		return fmt.Errorf("更新 Codex thread 配置: %w", err)
	} else {
		a.mergeCodexThreadConfigAt(threadID, CodexThreadConfig{
			Model: model, Effort: effort, ServiceTier: serviceTier, ServiceTierKnown: serviceTierSet,
		}, sequence)
	}
	return nil
}

// cacheCodexThreadConfigFromLifecycleResult 从 thread/start 或 thread/resume 响应同步权威配置。
func (a *ACPAgent) cacheCodexThreadConfigFromLifecycleResult(result json.RawMessage, threadID string, fallback CodexThreadConfig, sequence uint64) {
	var response struct {
		Model           string          `json:"model"`
		ReasoningEffort json.RawMessage `json:"reasoningEffort"`
		ServiceTier     json.RawMessage `json:"serviceTier"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return
	}
	config := CodexThreadConfig{Model: strings.TrimSpace(response.Model)}
	if len(response.ReasoningEffort) > 0 && string(response.ReasoningEffort) != "null" {
		var effort string
		if err := json.Unmarshal(response.ReasoningEffort, &effort); err != nil {
			return
		}
		config.Effort = strings.TrimSpace(effort)
	}
	if config.Model == "" {
		config.Model = strings.TrimSpace(fallback.Model)
	}
	if len(response.ReasoningEffort) == 0 {
		config.Effort = strings.TrimSpace(fallback.Effort)
	}
	if len(response.ServiceTier) > 0 {
		config.ServiceTierKnown = true
		if string(response.ServiceTier) != "null" {
			if err := json.Unmarshal(response.ServiceTier, &config.ServiceTier); err != nil {
				return
			}
			config.ServiceTier = normalizeCodexServiceTier(config.ServiceTier)
		}
	} else if fallback.ServiceTierKnown {
		config.ServiceTier = normalizeCodexServiceTier(fallback.ServiceTier)
		config.ServiceTierKnown = true
	}
	if config.Model == "" && config.Effort == "" && !config.ServiceTierKnown {
		return
	}
	a.setCodexThreadConfigAt(threadID, config, sequence)
}

// handleCodexThreadSettingsUpdated 接收其他 app-server 前端对同一 thread 的设置变更。
func (a *ACPAgent) handleCodexThreadSettingsUpdated(params json.RawMessage, sequence uint64) {
	var notification struct {
		ThreadID       string `json:"threadId"`
		ThreadSettings struct {
			Model       string          `json:"model"`
			Effort      *string         `json:"effort"`
			ServiceTier json.RawMessage `json:"serviceTier"`
		} `json:"threadSettings"`
	}
	if err := json.Unmarshal(params, &notification); err != nil {
		return
	}
	config := CodexThreadConfig{Model: strings.TrimSpace(notification.ThreadSettings.Model)}
	if notification.ThreadSettings.Effort != nil {
		config.Effort = strings.TrimSpace(*notification.ThreadSettings.Effort)
	}
	if len(notification.ThreadSettings.ServiceTier) > 0 {
		config.ServiceTierKnown = true
		if string(notification.ThreadSettings.ServiceTier) != "null" {
			if err := json.Unmarshal(notification.ThreadSettings.ServiceTier, &config.ServiceTier); err != nil {
				return
			}
			config.ServiceTier = normalizeCodexServiceTier(config.ServiceTier)
		}
	}
	if strings.TrimSpace(notification.ThreadID) == "" || config.Model == "" {
		return
	}
	a.setCodexThreadConfigAt(notification.ThreadID, config, sequence)
}

func (a *ACPAgent) setCodexThreadConfigAt(threadID string, config CodexThreadConfig, sequence uint64) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	a.mu.Lock()
	if a.codexThreadConfigs == nil {
		a.codexThreadConfigs = make(map[string]CodexThreadConfig)
	}
	if a.codexThreadConfigRevisions == nil {
		a.codexThreadConfigRevisions = make(map[string]uint64)
	}
	if sequence > 0 && a.codexThreadConfigRevisions[threadID] > sequence {
		a.mu.Unlock()
		return
	}
	a.codexThreadConfigs[threadID] = config
	if sequence > 0 {
		a.codexThreadConfigRevisions[threadID] = sequence
	}
	a.mu.Unlock()
}

func (a *ACPAgent) mergeCodexThreadConfigAt(threadID string, update CodexThreadConfig, sequence uint64) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	a.mu.Lock()
	if a.codexThreadConfigs == nil {
		a.codexThreadConfigs = make(map[string]CodexThreadConfig)
	}
	if a.codexThreadConfigRevisions == nil {
		a.codexThreadConfigRevisions = make(map[string]uint64)
	}
	if sequence > 0 && a.codexThreadConfigRevisions[threadID] > sequence {
		a.mu.Unlock()
		return
	}
	config := a.codexThreadConfigs[threadID]
	if strings.TrimSpace(update.Model) != "" {
		config.Model = strings.TrimSpace(update.Model)
	}
	if strings.TrimSpace(update.Effort) != "" {
		config.Effort = strings.TrimSpace(update.Effort)
	}
	if update.ServiceTierKnown {
		config.ServiceTier = normalizeCodexServiceTier(update.ServiceTier)
		config.ServiceTierKnown = true
	}
	a.codexThreadConfigs[threadID] = config
	if sequence > 0 {
		a.codexThreadConfigRevisions[threadID] = sequence
	}
	a.mu.Unlock()
}

// ListCodexModels 通过 Codex app-server 的 model/list 查询可用模型。
func (a *ACPAgent) ListCodexModels(ctx context.Context) ([]CodexModel, error) {
	if a.protocol != protocolCodexAppServer {
		return nil, fmt.Errorf("当前 Agent 不支持 Codex model/list")
	}
	result, err := a.rpc(ctx, "model/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	return parseCodexModelList(result)
}

// parseCodexModelList 兼容 Codex app-server 不同版本的模型列表字段命名。
func parseCodexModelList(data json.RawMessage) ([]CodexModel, error) {
	items, err := rawCodexModelItems(data)
	if err != nil {
		return nil, err
	}
	models := make([]CodexModel, 0, len(items))
	for _, item := range items {
		model, ok := parseCodexModel(item)
		if ok {
			models = append(models, model)
		}
	}
	return models, nil
}

// rawCodexModelItems 提取模型数组，兼容顶层数组、data 和 models 三种响应。
func rawCodexModelItems(data json.RawMessage) ([]json.RawMessage, error) {
	var wrapped struct {
		Models []json.RawMessage `json:"models"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Models != nil {
		return wrapped.Models, nil
	}
	if wrapped.Data != nil {
		return wrapped.Data, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse model/list result: %w", err)
	}
	return items, nil
}

// parseCodexModel 从单个模型对象中提取展示所需的最小字段。
func parseCodexModel(data json.RawMessage) (CodexModel, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return CodexModel{}, false
	}
	model := CodexModel{
		ID:            firstJSONText(fields, "id", "model", "slug"),
		Name:          firstJSONText(fields, "displayName", "name", "label"),
		EffortOptions: firstJSONStringList(fields, "supportedReasoningEfforts", "effort_options", "effortOptions", "efforts", "supportedEfforts"),
		ServiceTiers:  parseCodexServiceTiers(fields),
	}
	_ = json.Unmarshal(fields["isDefault"], &model.Default)
	return model, model.ID != ""
}

func parseCodexServiceTiers(fields map[string]json.RawMessage) []CodexServiceTier {
	var objects []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if json.Unmarshal(fields["serviceTiers"], &objects) == nil && len(objects) > 0 {
		tiers := make([]CodexServiceTier, 0, len(objects))
		for _, object := range objects {
			if id := normalizeCodexServiceTier(object.ID); id != "" {
				tiers = append(tiers, CodexServiceTier{ID: id, Name: object.Name, Description: object.Description})
			}
		}
		return tiers
	}
	legacy := firstJSONStringList(fields, "additionalSpeedTiers")
	tiers := make([]CodexServiceTier, 0, len(legacy))
	for _, value := range legacy {
		if id := normalizeCodexServiceTier(value); id != "" {
			tiers = append(tiers, CodexServiceTier{ID: id, Name: value})
		}
	}
	return tiers
}

func normalizeCodexServiceTier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case CodexServiceTierFast, "fast":
		return CodexServiceTierFast
	case CodexServiceTierStandard, "standard":
		return CodexServiceTierStandard
	default:
		return value
	}
}

// firstJSONText 按候选字段顺序读取第一个字符串值。
func firstJSONText(fields map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw, ok := fields[key]; ok && json.Unmarshal(raw, &value) == nil {
			return value
		}
	}
	return ""
}

// firstJSONStringList 按候选字段顺序读取第一个非空字符串列表。
func firstJSONStringList(fields map[string]json.RawMessage, keys ...string) []string {
	for _, key := range keys {
		values := jsonStringList(fields[key])
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

// jsonStringList 兼容字符串数组和带 reasoningEffort/value/id/name 的对象数组。
func jsonStringList(data json.RawMessage) []string {
	var strings []string
	if len(data) == 0 {
		return nil
	}
	if json.Unmarshal(data, &strings) == nil {
		return strings
	}
	var objects []map[string]json.RawMessage
	if json.Unmarshal(data, &objects) != nil {
		return nil
	}
	values := make([]string, 0, len(objects))
	for _, object := range objects {
		if value := firstJSONText(object, "reasoningEffort", "value", "id", "name", "label"); value != "" {
			values = append(values, value)
		}
	}
	return values
}
