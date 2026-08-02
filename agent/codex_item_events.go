package agent

import (
	"encoding/json"
	"strings"
)

type codexItemLifecycleParams struct {
	ThreadID string             `json:"threadId"`
	Item     codexLifecycleItem `json:"item"`
}

type codexLifecycleItem struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Command permissionCommand `json:"command"`
	Status  string            `json:"status"`
	Changes json.RawMessage   `json:"changes"`
	Server  string            `json:"server"`
	Tool    string            `json:"tool"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// handleCodexItemStarted 把完整 item 快照转换成最终文本或当前动作进度。
func (a *ACPAgent) handleCodexItemStarted(params json.RawMessage) {
	a.handleCodexItemStartedAt(params, 0)
}

func (a *ACPAgent) handleCodexItemStartedAt(params json.RawMessage, sequence uint64) {
	p, ok := decodeCodexItemLifecycle(params)
	if !ok {
		return
	}
	if p.Item.Type == "agentMessage" {
		a.dispatchCodexItemText(p, "", sequence)
		return
	}
	if strings.TrimSpace(p.Item.Status) == "" {
		p.Item.Status = "running"
	}
	a.dispatchCodexItemProgress(p, sequence)
}

func (a *ACPAgent) handleCodexItemCompletedAt(params json.RawMessage, sequence uint64) {
	p, ok := decodeCodexItemLifecycle(params)
	if !ok {
		return
	}
	if p.Item.Type == "agentMessage" {
		a.dispatchCodexItemText(p, "item_completed", sequence)
		return
	}
	if strings.TrimSpace(p.Item.Status) == "" {
		p.Item.Status = "completed"
	}
	a.dispatchCodexItemProgress(p, sequence)
}

func decodeCodexItemLifecycle(params json.RawMessage) (codexItemLifecycleParams, bool) {
	var p codexItemLifecycleParams
	return p, json.Unmarshal(params, &p) == nil
}

func (a *ACPAgent) dispatchCodexItemText(p codexItemLifecycleParams, kind string, sequence uint64) {
	for _, content := range p.Item.Content {
		if content.Type == "text" && content.Text != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: kind, ItemID: p.Item.ID, Text: content.Text, Sequence: sequence})
		}
	}
}

func (a *ACPAgent) dispatchCodexItemProgress(p codexItemLifecycleParams, sequence uint64) {
	progress := codexProgressParams{
		ThreadID: p.ThreadID,
		Status:   p.Item.Status,
		Command:  p.Item.Command,
		Changes:  p.Item.Changes,
	}
	switch p.Item.Type {
	case "commandExecution":
		a.dispatchCodexCommandProgress(progress, sequence)
	case "fileChange":
		a.dispatchCodexFileProgress(progress, sequence)
	case "mcpToolCall", "webSearch", "collabAgentToolCall":
		a.dispatchCodexLifecycleToolProgress(p, sequence)
	}
}

func (a *ACPAgent) dispatchCodexLifecycleToolProgress(p codexItemLifecycleParams, sequence uint64) {
	action := codexLifecycleToolAction(p.Item)
	if action == "" {
		return
	}
	a.dispatchProgressEventToThread(p.ThreadID, codexProgressPrefix+action, &codexProgressEvent{
		ID: codexLifecycleToolProgressID(p.Item), Kind: "tool", Action: action, Status: p.Item.Status,
	}, sequence)
}

// codexLifecycleToolAction 只展示协议声明的工具类型和名称，不透传参数、查询、结果或子智能体提示词。
func codexLifecycleToolAction(item codexLifecycleItem) string {
	switch item.Type {
	case "mcpToolCall":
		if strings.EqualFold(strings.TrimSpace(item.Server), "codegraph") {
			return "分析项目"
		}
		return "调用工具"
	case "webSearch":
		return "检索资料"
	case "collabAgentToolCall":
		switch normalizeCodexLifecycleToolName(item.Tool) {
		case "spawnagent":
			return "启动子智能体"
		case "sendmessage":
			return "向子智能体发送消息"
		case "wait":
			return "等待子智能体"
		case "resumeagent":
			return "恢复子智能体"
		case "interruptagent":
			return "中断子智能体"
		case "closeagent":
			return "关闭子智能体"
		default:
			return "调用子智能体"
		}
	default:
		return ""
	}
}

func codexLifecycleToolProgressID(item codexLifecycleItem) string {
	switch item.Type {
	case "mcpToolCall":
		if strings.EqualFold(strings.TrimSpace(item.Server), "codegraph") {
			return "tool:analysis"
		}
		return "tool:external"
	case "webSearch":
		return "tool:research"
	case "collabAgentToolCall":
		return "tool:collaboration"
	default:
		return "tool"
	}
}

func normalizeCodexLifecycleToolName(name string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(name)))
}
