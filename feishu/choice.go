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
	actions := make([]map[string]any, 0, len(choices))
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		label := strings.TrimSpace(choice.Label)
		if id == "" || label == "" {
			continue
		}
		actions = append(actions, map[string]any{
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
	if len(actions) == 0 {
		return "", fmt.Errorf("choice card requires at least one valid choice")
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "WeClaw",
			},
			"template": "blue",
		},
		"elements": []map[string]any{
			{
				"tag":     "markdown",
				"content": prompt,
			},
			{
				"tag":     "action",
				"actions": actions,
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal feishu choice card: %w", err)
	}
	return string(data), nil
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
