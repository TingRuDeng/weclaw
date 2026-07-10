package agent

import "encoding/json"

type codexItemLifecycleParams struct {
	ThreadID string             `json:"threadId"`
	Item     codexLifecycleItem `json:"item"`
}

type codexLifecycleItem struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Command          permissionCommand `json:"command"`
	Cwd              string            `json:"cwd"`
	Status           string            `json:"status"`
	AggregatedOutput string            `json:"aggregatedOutput"`
	Changes          json.RawMessage   `json:"changes"`
	Content          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// handleCodexItemStarted 把完整 item 快照转换成最终文本或当前动作进度。
func (a *ACPAgent) handleCodexItemStarted(params json.RawMessage) {
	p, ok := decodeCodexItemLifecycle(params)
	if !ok {
		return
	}
	if p.Item.Type == "agentMessage" {
		a.dispatchCodexItemText(p, "")
		return
	}
	a.dispatchCodexItemProgress(p)
}

// handleCodexItemCompleted 把最终 item 作为正文兜底，并刷新命令或文件终态。
func (a *ACPAgent) handleCodexItemCompleted(params json.RawMessage) {
	p, ok := decodeCodexItemLifecycle(params)
	if !ok {
		return
	}
	if p.Item.Type == "agentMessage" {
		a.dispatchCodexItemText(p, "item_completed")
		return
	}
	a.dispatchCodexItemProgress(p)
}

func decodeCodexItemLifecycle(params json.RawMessage) (codexItemLifecycleParams, bool) {
	var p codexItemLifecycleParams
	return p, json.Unmarshal(params, &p) == nil
}

func (a *ACPAgent) dispatchCodexItemText(p codexItemLifecycleParams, kind string) {
	for _, content := range p.Item.Content {
		if content.Type == "text" && content.Text != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: kind, ItemID: p.Item.ID, Text: content.Text})
		}
	}
}

func (a *ACPAgent) dispatchCodexItemProgress(p codexItemLifecycleParams) {
	progress := codexProgressParams{
		ThreadID: p.ThreadID,
		Status:   p.Item.Status,
		Output:   p.Item.AggregatedOutput,
		Command:  p.Item.Command,
		Cwd:      p.Item.Cwd,
		Changes:  p.Item.Changes,
	}
	switch p.Item.Type {
	case "commandExecution":
		a.dispatchCodexCommandProgress(progress)
	case "fileChange":
		a.dispatchCodexFileProgress(progress)
	}
}
