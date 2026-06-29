package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	cardStatusThinking  = "thinking"
	cardStatusStreaming = "streaming"
	cardStatusDone      = "done"
	cardStatusError     = "error"
	cardMainContentID   = "main_content"
)

type cardOptions struct {
	Status  string
	Title   string
	Content string
}

// buildCardV2 构建飞书 CardKit 2.0 卡片 JSON，状态和正文使用稳定 element_id 便于后续流式更新。
func buildCardV2(opts cardOptions) (string, error) {
	status := normalizeCardStatus(opts.Status)
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "WeClaw"
	}
	content := strings.TrimSpace(opts.Content)
	if content == "" {
		content = statusDefaultContent(status)
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
			"elements": []map[string]any{
				{
					"tag":        "markdown",
					"element_id": "status",
					"content":    statusLabel(status),
				},
				{
					"tag":        "markdown",
					"element_id": cardMainContentID,
					"content":    content,
				},
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal feishu card: %w", err)
	}
	return string(data), nil
}

// normalizeCardStatus 收敛未知状态，避免生成不可识别样式。
func normalizeCardStatus(status string) string {
	switch status {
	case cardStatusThinking, cardStatusStreaming, cardStatusDone, cardStatusError:
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
	default:
		return "正在分析任务，请稍候。"
	}
}
