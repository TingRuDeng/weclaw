package feishu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	cardActionChoice   = "choice"
	cardActionStop     = "stop"
	cardKindApproval   = "approval"
	approvalPromptHead = "Codex 请求执行敏感操作，请确认："
)

type parsedCardAction struct {
	Action    string
	Choice    string
	Kind      string
	Label     string
	Conv      string
	UserID    string
	ChatID    string
	MessageID string
}

// buildChoiceCard 构建飞书按钮卡片，每个按钮携带可回放到业务层的动作值。
func buildChoiceCard(prompt string, choices []platform.Choice, conversationKey string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "请选择："
	}
	buttons := buildChoiceButtons(choices, conversationKey, choiceCardKind(prompt))
	if len(buttons) == 0 {
		return "", fmt.Errorf("choice card requires at least one valid choice")
	}
	elements := []map[string]any{
		{
			"tag":       "markdown",
			"content":   prompt,
			"text_size": "normal",
		},
	}
	elements = append(elements, buttons...)
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
			"template": "blue",
		},
		"body": map[string]any{
			"direction": "vertical",
			"elements":  elements,
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal feishu choice card: %w", err)
	}
	return string(data), nil
}

// buildChoiceButtons 过滤无效选项，并生成 CardKit 2.0 可点击按钮元素。
func buildChoiceButtons(choices []platform.Choice, conversationKey string, kind string) []map[string]any {
	buttons := make([]map[string]any, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		label := strings.TrimSpace(choice.Label)
		if id == "" || label == "" {
			continue
		}
		value := map[string]string{
			"action": cardActionChoice,
			"choice": id,
			"conv":   conversationKey,
			"label":  label,
		}
		if kind != "" {
			value["kind"] = kind
		}
		buttons = append(buttons, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": label,
			},
			"type":  "primary",
			"value": value,
		})
	}
	return buttons
}

// choiceCardKind 只标记 Codex 审批卡片，避免普通导航/选择卡片点击后被改成审批状态。
func choiceCardKind(prompt string) string {
	if strings.HasPrefix(strings.TrimSpace(prompt), approvalPromptHead) {
		return cardKindApproval
	}
	return ""
}

// parseCardAction 将飞书回调事件归一化为平台 RawCommand 需要的字段。
func parseCardAction(event *callback.CardActionTriggerEvent) (parsedCardAction, bool) {
	if event == nil || event.Event == nil || event.Event.Action == nil || event.Event.Operator == nil {
		return parsedCardAction{}, false
	}
	value := event.Event.Action.Value
	action := callbackValueString(value, "action")
	if action == "" {
		return parsedCardAction{}, false
	}
	parsed := parsedCardAction{
		Action: action,
		Choice: callbackValueString(value, "choice"),
		Kind:   callbackValueString(value, "kind"),
		Label:  callbackValueString(value, "label"),
		Conv:   callbackValueString(value, "conv"),
		UserID: strings.TrimSpace(event.Event.Operator.OpenID),
	}
	if event.Event.Context != nil {
		parsed.ChatID = strings.TrimSpace(event.Event.Context.OpenChatID)
		parsed.MessageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	if parsed.UserID == "" {
		return parsedCardAction{}, false
	}
	return parsed, true
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
			"template": "green",
		},
		"body": map[string]any{
			"direction": "vertical",
			"elements": []map[string]any{
				{
					"tag":       "markdown",
					"content":   fmt.Sprintf("**已处理**\n\n已选择：%s", label),
					"text_size": "normal",
				},
			},
		},
	}
	return &callback.Card{Type: "card_json", Data: card}
}

func callbackValueString(value map[string]interface{}, key string) string {
	if value == nil {
		return ""
	}
	raw, ok := value[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
