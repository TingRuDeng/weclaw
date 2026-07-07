package feishu

import (
	"fmt"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

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
		Action:  action,
		Choice:  callbackValueString(value, "choice"),
		Kind:    callbackValueString(value, "kind"),
		Label:   callbackValueString(value, "label"),
		Summary: callbackValueString(value, "summary"),
		TaskCard: firstNonEmpty(
			callbackValueString(value, "task_card_id"),
			callbackValueString(value, "taskCardId"),
		),
		Approval: firstNonEmpty(
			callbackValueString(value, "approval_key"),
			callbackValueString(value, "approval_id"),
			callbackValueString(value, "approvalId"),
			callbackValueString(value, "action_key"),
			callbackValueString(value, "actionKey"),
		),
		Owner:       callbackValueString(value, approvalOwnerValueKey),
		Panel:       callbackValueString(value, "approval_panel") == "1",
		Conv:        callbackValueString(value, "conv"),
		SessionKey:  callbackValueString(value, feishuSessionMetadataKey),
		UserID:      strings.TrimSpace(event.Event.Operator.OpenID),
		UserAliases: cardActionUserAliases(event.Event.Operator),
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

// cardActionUserAliases 抽取飞书卡片回调里除 open_id 外的用户身份。
func cardActionUserAliases(operator *callback.Operator) []string {
	if operator == nil {
		return nil
	}
	aliases := make([]string, 0, 1)
	seen := make(map[string]bool, 1)
	addFeishuAlias(&aliases, seen, stringValue(operator.UserID))
	return aliases
}

func firstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok {
			value := strings.TrimSpace(fmt.Sprint(raw))
			if value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
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
