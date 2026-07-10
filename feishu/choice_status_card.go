package feishu

import (
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// buildSelectedChoiceCard 将已点击的普通交互卡片收纳为不可重复操作的选择结果。
func buildSelectedChoiceCard(action parsedCardAction) *callback.Card {
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = strings.TrimSpace(action.Choice)
	}
	if label == "" {
		label = "已选择"
	}
	return buildChoiceHandledStatusCard("blue", "已选择："+label)
}

// buildChoiceHandledCard 构建按钮点击后的原卡片替换内容，让用户能区分已处理审批。
func buildChoiceHandledCard(action parsedCardAction) *callback.Card {
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = strings.TrimSpace(action.Choice)
	}
	if label == "" {
		label = "已选择"
	}
	status, template := approvalHandledStatus(action)
	if strings.TrimSpace(action.Status) == approvalStatusArchived {
		return buildChoiceHandledStatusCard(template, "**"+status+"**")
	}
	content := "**" + status + "**\n\n已选择：" + label
	return buildChoiceHandledStatusCard(template, content)
}

func buildChoiceHandledStatusCard(template string, content string) *callback.Card {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":     true,
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "WeClaw",
			},
			"template": template,
		},
		"body": map[string]any{
			"direction": "vertical",
			"elements": []map[string]any{
				{
					"tag":       "markdown",
					"content":   content,
					"text_size": "normal",
				},
			},
		},
	}
	return &callback.Card{Type: "raw", Data: card}
}

func approvalHandledStatus(action parsedCardAction) (string, string) {
	if strings.TrimSpace(action.Status) == approvalStatusArchived {
		return "✅ 已收纳到任务卡片", "green"
	}
	if strings.TrimSpace(action.Status) == approvalStatusExpired {
		return "⚠️ 已过期", "yellow"
	}
	choice := strings.ToLower(strings.TrimSpace(action.Choice))
	label := strings.ToLower(strings.TrimSpace(action.Label))
	switch {
	case strings.Contains(choice, "cancel") ||
		strings.Contains(choice, "deny") ||
		strings.Contains(choice, "reject") ||
		strings.Contains(label, "cancel") ||
		strings.Contains(label, "拒"):
		return "❌ 已拒绝", "red"
	default:
		return "✅ 已授权", "green"
	}
}
