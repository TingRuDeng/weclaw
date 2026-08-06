package feishu

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
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
	return buildChoiceHandledStatusCardWithTitle(
		"blue", choiceHandledCardTitle(action),
		"已受理："+label+"\n\n"+choicePendingDetail(action.Choice),
	)
}

func buildInlineChoiceCompletedCard(action parsedCardAction) *callback.Card {
	label := strings.TrimSpace(action.Label)
	if label == "" {
		label = strings.TrimSpace(action.Choice)
	}
	if label == "" {
		label = "该操作"
	}
	return buildChoiceHandledStatusCardWithTitle(
		"green", choiceHandledCardTitle(action), "已完成："+label,
	)
}

func choicePendingDetail(choice string) string {
	command := strings.ToLower(strings.TrimSpace(choice))
	switch {
	case command == "/cx cd" || strings.HasPrefix(command, "/cx cd "):
		return "正在加载该工作空间的会话；如只有一个会话将自动切换，完成后将在本卡片更新结果。"
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
	return buildChoiceHandledStatusCardWithTitle(template, "WeClaw", content)
}

func buildChoiceHandledStatusCardWithTitle(template string, title string, content string) *callback.Card {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "WeClaw"
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":     true,
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
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

func choiceHandledCardTitle(action parsedCardAction) string {
	if strings.TrimSpace(action.Kind) == platform.ChoiceInteractionTaskControl {
		agentName := strings.TrimSpace(action.AgentName)
		if agentName == "" {
			agentName = "Agent"
		}
		return agentName + " · 暂存消息"
	}
	return "WeClaw"
}

// choiceCommandResultTemplate 为具备明确处理结果的切换和任务控制命令着色。
func choiceCommandResultTemplate(command string, content string) string {
	if isTaskControlCommand(command) {
		return taskControlCommandResultTemplate(command, content)
	}
	if isModelSettingCommand(command) && modelSettingCommandSucceeded(content) {
		return "green"
	}
	if !isDeferredCardResultCommand(command) {
		return "blue"
	}
	content = strings.TrimSpace(content)
	if strings.Contains(content, "等待超时") {
		return "yellow"
	}
	if strings.Contains(content, "当前工作空间没有可用会话") {
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

func isModelSettingCommand(command string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/model", "/reasoning":
		return true
	default:
		return false
	}
}

func modelSettingCommandSucceeded(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "已将") && strings.Contains(content, "切换为")
}

func isTaskControlCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "/guide", "/cancel", "/stop":
		return true
	default:
		return false
	}
}

func taskControlCommandResultTemplate(command string, content string) string {
	content = strings.TrimSpace(content)
	switch {
	case strings.Contains(content, "已处理") || strings.Contains(content, "已经过期") ||
		strings.Contains(content, "当前没有") || strings.Contains(content, "已经结束"):
		return "yellow"
	case strings.EqualFold(strings.TrimSpace(command), "/stop") &&
		(strings.Contains(content, "等待任务终态") || strings.Contains(content, "已受理")):
		return "yellow"
	case strings.Contains(content, "已发送到") || strings.Contains(content, "已撤回") ||
		strings.Contains(content, "已停止"):
		return "green"
	default:
		return "red"
	}
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
	if strings.TrimSpace(action.Status) == approvalStatusAutoApproved {
		return "✅ 已自动批准（YOLO）", "green"
	}
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
