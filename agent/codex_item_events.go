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
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// handleCodexItemStarted 只提取用户可见的 agentMessage 文本。
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
}

func decodeCodexItemLifecycle(params json.RawMessage) (codexItemLifecycleParams, bool) {
	var p codexItemLifecycleParams
	return p, json.Unmarshal(params, &p) == nil
}

func (a *ACPAgent) dispatchCodexItemText(p codexItemLifecycleParams, kind string, sequence uint64) {
	if text := strings.TrimSpace(p.Item.Text); text != "" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: kind, ItemID: p.Item.ID, Text: text, Sequence: sequence})
		return
	}
	var parts []string
	for _, content := range p.Item.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{Kind: kind, ItemID: p.Item.ID, Text: text, Sequence: sequence})
	}
}
