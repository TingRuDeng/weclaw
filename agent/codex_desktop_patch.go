package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

type codexDesktopPatch struct {
	Op    string `json:"op"`
	Path  []any  `json:"path"`
	Value any    `json:"value,omitempty"`
}

// applyCodexDesktopPatches 在私有副本应用整组 patch，失败时不修改输入。
func applyCodexDesktopPatches(baseline map[string]any, patches []codexDesktopPatch) (map[string]any, error) {
	var root any = cloneCodexDesktopJSON(baseline)
	for index, patch := range patches {
		next, err := applyCodexDesktopPatch(root, patch)
		if err != nil {
			return nil, fmt.Errorf("应用 Codex Desktop patch[%d]: %w", index, err)
		}
		root = next
	}
	result, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Codex Desktop patch 根节点必须是对象")
	}
	return result, nil
}

// applyCodexDesktopPatch 应用单个 patch，并限制根节点操作。
func applyCodexDesktopPatch(root any, patch codexDesktopPatch) (any, error) {
	if len(patch.Path) == 0 {
		if patch.Op != "add" && patch.Op != "replace" {
			return nil, fmt.Errorf("根节点不支持操作 %q", patch.Op)
		}
		if _, ok := patch.Value.(map[string]any); !ok {
			return nil, fmt.Errorf("根节点值必须是对象")
		}
		return cloneCodexDesktopJSON(patch.Value), nil
	}
	return applyCodexDesktopPatchAt(root, patch.Path, patch)
}

// applyCodexDesktopPatchAt 递归返回更新节点，使数组扩缩容能回写父容器。
func applyCodexDesktopPatchAt(node any, path []any, patch codexDesktopPatch) (any, error) {
	if len(path) == 1 {
		return applyCodexDesktopPatchLeaf(node, path[0], patch)
	}
	child, err := codexDesktopPatchChild(node, path[0])
	if err != nil {
		return nil, err
	}
	next, err := applyCodexDesktopPatchAt(child, path[1:], patch)
	if err != nil {
		return nil, err
	}
	return replaceCodexDesktopPatchChild(node, path[0], next)
}

// codexDesktopPatchChild 读取路径中的现有对象 key 或数组 index。
func codexDesktopPatchChild(node any, part any) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		key, ok := part.(string)
		child, exists := typed[key]
		if !ok || !exists {
			return nil, fmt.Errorf("对象路径 %v 不存在", part)
		}
		return child, nil
	case []any:
		index, err := codexDesktopPatchIndex(part, len(typed), false)
		if err != nil {
			return nil, err
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("路径节点 %v 不是容器", part)
	}
}

// replaceCodexDesktopPatchChild 把递归结果写回当前容器。
func replaceCodexDesktopPatchChild(node any, part any, child any) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		key, ok := part.(string)
		if !ok {
			return nil, fmt.Errorf("对象路径必须是字符串")
		}
		typed[key] = child
		return typed, nil
	case []any:
		index, err := codexDesktopPatchIndex(part, len(typed), false)
		if err != nil {
			return nil, err
		}
		typed[index] = child
		return typed, nil
	default:
		return nil, fmt.Errorf("路径节点不是容器")
	}
}

// applyCodexDesktopPatchLeaf 把叶子操作分派到对象或数组语义。
func applyCodexDesktopPatchLeaf(node any, part any, patch codexDesktopPatch) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		return applyCodexDesktopObjectPatch(typed, part, patch)
	case []any:
		return applyCodexDesktopArrayPatch(typed, part, patch)
	default:
		return nil, fmt.Errorf("patch 目标不是容器")
	}
}

// applyCodexDesktopObjectPatch 校验 key 存在性后执行对象操作。
func applyCodexDesktopObjectPatch(node map[string]any, part any, patch codexDesktopPatch) (any, error) {
	key, ok := part.(string)
	if !ok {
		return nil, fmt.Errorf("对象路径必须是字符串")
	}
	_, exists := node[key]
	switch patch.Op {
	case "add":
		node[key] = cloneCodexDesktopJSON(patch.Value)
	case "replace":
		if !exists {
			return nil, fmt.Errorf("replace 目标 %q 不存在", key)
		}
		node[key] = cloneCodexDesktopJSON(patch.Value)
	case "remove":
		if !exists {
			return nil, fmt.Errorf("remove 目标 %q 不存在", key)
		}
		delete(node, key)
	default:
		return nil, fmt.Errorf("不支持 patch 操作 %q", patch.Op)
	}
	return node, nil
}

// applyCodexDesktopArrayPatch 按 Immer 语义插入、替换或删除数组项。
func applyCodexDesktopArrayPatch(node []any, part any, patch codexDesktopPatch) (any, error) {
	allowEnd := patch.Op == "add"
	index, err := codexDesktopPatchIndex(part, len(node), allowEnd)
	if err != nil {
		return nil, err
	}
	switch patch.Op {
	case "add":
		value := cloneCodexDesktopJSON(patch.Value)
		node = append(node, nil)
		copy(node[index+1:], node[index:])
		node[index] = value
	case "replace":
		node[index] = cloneCodexDesktopJSON(patch.Value)
	case "remove":
		copy(node[index:], node[index+1:])
		node[len(node)-1] = nil
		node = node[:len(node)-1]
	default:
		return nil, fmt.Errorf("不支持 patch 操作 %q", patch.Op)
	}
	return node, nil
}

// codexDesktopPatchIndex 校验整数索引和 add 允许的尾部位置。
func codexDesktopPatchIndex(part any, length int, allowEnd bool) (int, error) {
	index, ok := codexDesktopInteger(part)
	limit := length - 1
	if allowEnd {
		limit = length
	}
	if !ok || index < 0 || index > limit {
		return 0, fmt.Errorf("数组索引 %v 越界", part)
	}
	return index, nil
}

// codexDesktopInteger 兼容测试整数与 JSON 解码数字。
func codexDesktopInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		return int(typed), typed >= 0 && typed <= math.MaxInt && typed == math.Trunc(typed)
	case json.Number:
		parsed, err := strconv.Atoi(string(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

// cloneCodexDesktopJSON 保留标量类型并递归复制 JSON 容器。
func cloneCodexDesktopJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, child := range typed {
			clone[key] = cloneCodexDesktopJSON(child)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for index, child := range typed {
			clone[index] = cloneCodexDesktopJSON(child)
		}
		return clone
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	default:
		return typed
	}
}

// cloneCodexDesktopPatches 深拷贝等待队列中的 path 和 value。
func cloneCodexDesktopPatches(patches []codexDesktopPatch) []codexDesktopPatch {
	cloned := make([]codexDesktopPatch, len(patches))
	for index, patch := range patches {
		cloned[index] = codexDesktopPatch{
			Op: patch.Op, Path: append([]any(nil), patch.Path...),
			Value: cloneCodexDesktopJSON(patch.Value),
		}
	}
	return cloned
}
