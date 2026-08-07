package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/observability"
)

type codexItemLifecycleParams struct {
	ThreadID string             `json:"threadId"`
	Item     codexLifecycleItem `json:"item"`
}

type codexLifecycleItem struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Text      string            `json:"text"`
	Phase     string            `json:"phase"`
	Status    string            `json:"status"`
	Server    string            `json:"server"`
	Namespace string            `json:"namespace"`
	Tool      string            `json:"tool"`
	Changes   []json.RawMessage `json:"changes"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// handleCodexItemStartedAt 提取用户可见消息和允许展示的结构化生命周期摘要。
func (a *ACPAgent) handleCodexItemStartedAt(params json.RawMessage, sequence uint64) {
	p, ok := decodeCodexItemLifecycle(params)
	if !ok {
		return
	}
	if p.Item.Type == "agentMessage" {
		a.dispatchCodexItemText(p, "", sequence)
		return
	}
	a.dispatchCodexItemProgress(p, false, sequence)
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
	a.dispatchCodexItemProgress(p, true, sequence)
}

func decodeCodexItemLifecycle(params json.RawMessage) (codexItemLifecycleParams, bool) {
	var p codexItemLifecycleParams
	return p, json.Unmarshal(params, &p) == nil
}

func (a *ACPAgent) dispatchCodexItemText(p codexItemLifecycleParams, kind string, sequence uint64) {
	if text := strings.TrimSpace(p.Item.Text); text != "" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
			Kind: kind, ItemID: p.Item.ID, MessagePhase: strings.TrimSpace(p.Item.Phase), Text: text, Sequence: sequence,
		})
		return
	}
	var parts []string
	for _, content := range p.Item.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
		a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
			Kind: kind, ItemID: p.Item.ID, MessagePhase: strings.TrimSpace(p.Item.Phase), Text: text, Sequence: sequence,
		})
	}
}

func (a *ACPAgent) dispatchCodexItemProgress(p codexItemLifecycleParams, completed bool, sequence uint64) {
	if strings.EqualFold(strings.TrimSpace(p.Item.Type), "commandExecution") {
		if itemID := strings.TrimSpace(p.Item.ID); itemID != "" {
			a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
				Kind: "activity", ItemID: itemID, Sequence: sequence,
			})
		}
		return
	}
	event, ok := codexLifecycleProgressEvent(p.Item, completed, sequence)
	if !ok {
		return
	}
	a.dispatchToTurnCh(p.ThreadID, &codexTurnEvent{
		Kind: "progress", ItemID: p.Item.ID, Sequence: sequence, Progress: &event,
	})
}

func codexLifecycleProgressEvent(item codexLifecycleItem, completed bool, sequence uint64) (ProgressEvent, bool) {
	itemID := strings.TrimSpace(item.ID)
	if itemID == "" {
		return ProgressEvent{}, false
	}
	var kind ProgressKind
	var idPrefix, summary string
	switch item.Type {
	case "commandExecution":
		// 每条命令只会产生相同的泛化摘要，无法给用户提供有效进展；
		// 真正需要处理的命令审批仍通过独立 approval 事件展示。
		return ProgressEvent{}, false
	case "fileChange":
		kind, idPrefix, summary = ProgressKindFile, "file:", "修改文件"
		if len(item.Changes) > 1 {
			summary = fmt.Sprintf("修改 %d 个文件", len(item.Changes))
		}
	case "mcpToolCall":
		kind, idPrefix = ProgressKindTool, "tool:"
		summary = codexToolProgressSummary(item.Server, item.Tool)
	case "dynamicToolCall":
		kind, idPrefix = ProgressKindTool, "tool:"
		summary = codexToolProgressSummary(item.Namespace, item.Tool)
	default:
		return ProgressEvent{}, false
	}
	state := codexProgressState(item.Status, completed)
	return ProgressEvent{
		ID: idPrefix + itemID, Kind: kind, State: state, Sequence: sequence,
		Summary: summary, Text: summary,
	}, true
}

func codexToolProgressSummary(namespace string, tool string) string {
	parts := make([]string, 0, 2)
	if namespace = observability.SanitizeText(namespace); namespace != "" {
		parts = append(parts, namespace)
	}
	if tool = observability.SanitizeText(tool); tool != "" {
		parts = append(parts, tool)
	}
	if len(parts) == 0 {
		return "调用工具"
	}
	return "调用工具 " + strings.Join(parts, "/")
}

func codexProgressState(status string, completed bool) ProgressState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return ProgressStatePending
	case "inprogress", "running":
		return ProgressStateRunning
	case "completed":
		return ProgressStateCompleted
	case "failed", "declined", "cancelled", "canceled", "interrupted":
		return ProgressStateFailed
	default:
		if completed {
			return ProgressStateCompleted
		}
		return ProgressStateRunning
	}
}
