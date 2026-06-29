package feishu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	cardActionChoice = "choice"
	cardActionStop   = "stop"
)

type parsedCardAction struct {
	Action    string
	Choice    string
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
	buttons := buildChoiceButtons(choices, conversationKey)
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
func buildChoiceButtons(choices []platform.Choice, conversationKey string) []map[string]any {
	buttons := make([]map[string]any, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		label := strings.TrimSpace(choice.Label)
		if id == "" || label == "" {
			continue
		}
		buttons = append(buttons, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": label,
			},
			"type": "primary",
			"value": map[string]string{
				"action": cardActionChoice,
				"choice": id,
				"conv":   conversationKey,
			},
		})
	}
	return buttons
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
