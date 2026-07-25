package feishu

import (
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// buildSubmittedChoiceCard 收纳已点击卡片，并明确区分受理与最终业务成功。
func buildSubmittedChoiceCard(action parsedCardAction) *callback.Card {
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = strings.TrimSpace(action.Choice)
	}
	if label == "" {
		label = "已选择"
	}
	return buildChoiceHandledStatusCard("blue", "已受理："+label+"\n\n"+choicePendingDetail(action.Choice))
}

func buildInlineChoiceCompletedCard(action parsedCardAction) *callback.Card {
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = strings.TrimSpace(action.Choice)
	}
	if label == "" {
		label = "该操作"
	}
	return buildChoiceHandledStatusCard("green", "已完成："+label)
}

func choicePendingDetail(choice string) string {
	command := strings.ToLower(strings.TrimSpace(choice))
	switch {
	case command == "/cx cd" || strings.HasPrefix(command, "/cx cd "):
		return "正在加载该工作空间的会话列表，结果将单独发送。"
	case command == "/cx switch" || strings.HasPrefix(command, "/cx switch ") ||
		command == "/cc switch" || strings.HasPrefix(command, "/cc switch "):
		return "正在切换并接管，完成后将在本卡片更新结果。"
	case strings.HasPrefix(command, "/cx account confirm "):
		return "正在检查全局任务和写入状态，并切换共享 Codex Host；完成后将在本卡片更新结果。"
	default:
		return "正在处理，结果将单独发送。"
	}
}

// buildChoiceHandledCard 构建按钮点击后的原卡片替换内容，让用户能区分已处理审批。
func buildChoiceHandledCard(action parsedCardAction) *callback.Card {
	if strings.TrimSpace(action.Status) == approvalStatusPending {
		return buildSubmittedChoiceCard(action)
	}
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

// choiceCommandResultTemplate 只为具备明确终态的切换命令着色；普通信息卡保持蓝色。
func choiceCommandResultTemplate(command string, content string) string {
	if !isDeferredCardResultCommand(command) {
		return "blue"
	}
	content = strings.TrimSpace(content)
	if strings.Contains(content, "等待超时") {
		return "yellow"
	}
	if choiceCommandSucceeded(content) {
		if strings.Contains(content, "暂不可用") || strings.Contains(content, "运行通道: 不可用") {
			return "yellow"
		}
		return "green"
	}
	return "red"
}

func choiceCommandSucceeded(content string) bool {
	for _, marker := range []string{
		"已切换",
		"切换成功",
		"已进入工作空间并绑定",
		"当前已经是目标 Codex 账号",
	} {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func approvalHandledStatus(action parsedCardAction) (string, string) {
	if strings.TrimSpace(action.Status) == approvalStatusArchived {
		return "✅ 已收纳到任务卡片", "green"
	}
	if strings.TrimSpace(action.Status) == approvalStatusExpired {
		return "⚠️ 已过期", "yellow"
	}
	if strings.TrimSpace(action.Status) == approvalStatusUnconfirmed {
		return "⚠️ 处理结果未确认", "yellow"
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
