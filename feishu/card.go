package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	cardStatusThinking   = "thinking"
	cardStatusStreaming  = "streaming"
	cardStatusDone       = "done"
	cardStatusError      = "error"
	cardStatusSuperseded = "superseded"
	cardMainContentID    = "main_content"
)

type cardOptions struct {
	Status    string
	Title     string
	Content   string
	Approvals []string
}

// buildCardV2 构建飞书 CardKit 2.0 卡片 JSON，状态和正文使用稳定 element_id 便于后续流式更新。
func buildCardV2(opts cardOptions) (string, error) {
	status := normalizeCardStatus(opts.Status)
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "WeClaw"
	}
	content := strings.TrimSpace(opts.Content)
	omitMainContent := status == cardStatusDone && content == ""
	if content == "" && !omitMainContent {
		content = statusDefaultContent(status)
	}
	elements := []map[string]any{
		{
			"tag":        "markdown",
			"element_id": "status",
			"content":    statusLabel(status),
		},
	}
	if !omitMainContent {
		elements = append(elements, map[string]any{
			"tag":        "markdown",
			"element_id": cardMainContentID,
			"content":    content,
		})
	}
	if approvalContent := approvalRecordsContent(opts.Approvals); approvalContent != "" {
		elements = append(elements, map[string]any{
			"tag":        "markdown",
			"element_id": "approval_records",
			"content":    approvalContent,
		})
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode":   status == cardStatusStreaming || status == cardStatusThinking,
			"update_multi":     true,
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
			"template": statusTemplate(status),
		},
		"body": map[string]any{
			"direction": "vertical",
			"elements":  elements,
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal feishu card: %w", err)
	}
	return string(data), nil
}

func approvalRecordsContent(records []string) string {
	lines := make([]string, 0, len(records)+1)
	for _, record := range records {
		if trimmed := strings.TrimSpace(record); trimmed != "" {
			lines = append(lines, "- "+trimmed)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "**审批记录**\n" + strings.Join(lines, "\n")
}

// normalizeCardStatus 收敛未知状态，避免生成不可识别样式。
func normalizeCardStatus(status string) string {
	switch status {
	case cardStatusThinking, cardStatusStreaming, cardStatusDone, cardStatusError, cardStatusSuperseded:
		return status
	default:
		return cardStatusThinking
	}
}

// statusLabel 返回卡片顶部状态文案。
func statusLabel(status string) string {
	switch status {
	case cardStatusStreaming:
		return "**生成中**"
	case cardStatusDone:
		return "**已完成**"
	case cardStatusError:
		return "**执行失败**"
	case cardStatusSuperseded:
		return "**已转移**"
	default:
		return "**思考中**"
	}
}

// statusTemplate 返回飞书卡片 header 颜色模板。
func statusTemplate(status string) string {
	switch status {
	case cardStatusDone:
		return "green"
	case cardStatusError:
		return "red"
	case cardStatusSuperseded:
		return "grey"
	default:
		return "blue"
	}
}

// statusDefaultContent 返回空内容时的默认正文。
func statusDefaultContent(status string) string {
	switch status {
	case cardStatusDone:
		return "任务已完成。"
	case cardStatusError:
		return "任务执行失败。"
	case cardStatusStreaming:
		return "正在生成结果，请稍候。"
	case cardStatusSuperseded:
		return "已在新位置继续展示。"
	default:
		return "正在分析任务，请稍候。"
	}
}
