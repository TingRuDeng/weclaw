package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// CodexModelStatus 返回当前 WeClaw 侧配置，空值表示沿用 Codex 默认值。
func (a *ACPAgent) CodexModelStatus() CodexModelStatus {
	return CodexModelStatus{Model: a.model, Effort: a.effort}
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
	}
	return model, model.ID != ""
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
