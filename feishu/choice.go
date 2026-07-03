package feishu

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	cardActionChoice       = "choice"
	cardActionStop         = "stop"
	cardKindApproval       = "approval"
	approvalOwnerValueKey  = "approval_owner"
	approvalStatusHandled  = "handled"
	approvalStatusExpired  = "expired"
	approvalStatusArchived = "archived"
	approvalPromptHead     = "Codex 请求执行敏感操作，请确认："
	approvalSummaryMaxRune = 160
)

type parsedCardAction struct {
	Action     string
	Choice     string
	Kind       string
	Label      string
	Summary    string
	TaskCard   string
	Approval   string
	Owner      string
	Panel      bool
	Conv       string
	SessionKey string
	UserID     string
	ChatID     string
	MessageID  string
	Status     string
}

type choiceButtonOptions struct {
	ConversationKey string
	Kind            string
	Summary         string
}

// buildChoiceCard 构建飞书按钮卡片，每个按钮携带可回放到业务层的动作值。
func buildChoiceCard(prompt string, choices []platform.Choice, conversationKey string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "请选择："
	}
	options := choiceButtonOptions{
		ConversationKey: conversationKey,
		Kind:            choiceCardKind(prompt),
		Summary:         approvalSummaryFromPrompt(prompt),
	}
	buttons := buildChoiceButtons(choices, options)
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
func buildChoiceButtons(choices []platform.Choice, options choiceButtonOptions) []map[string]any {
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
			"conv":   options.ConversationKey,
			"label":  label,
		}
		if options.Kind != "" {
			value["kind"] = options.Kind
		}
		if options.Summary != "" {
			value["summary"] = options.Summary
		}
		if approvalKey := firstNonEmpty(strings.TrimSpace(choice.Metadata["approval_key"]), approvalPayloadKey(options)); approvalKey != "" {
			value["approval_key"] = approvalKey
		}
		if taskCardID := strings.TrimSpace(choice.Metadata["task_card_id"]); taskCardID != "" {
			value["task_card_id"] = taskCardID
		}
		if owner := strings.TrimSpace(choice.Metadata[approvalOwnerValueKey]); owner != "" {
			value[approvalOwnerValueKey] = owner
		}
		if sessionKey := strings.TrimSpace(choice.Metadata[feishuSessionMetadataKey]); sessionKey != "" {
			value[feishuSessionMetadataKey] = sessionKey
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

// approvalPayloadKey 给同一张审批卡片上的所有按钮生成同一个稳定 key，避免不同 decision 互相覆盖。
func approvalPayloadKey(options choiceButtonOptions) string {
	if options.Kind != cardKindApproval {
		return ""
	}
	base := strings.TrimSpace(options.ConversationKey) + "\x00" + strings.TrimSpace(options.Summary)
	if strings.TrimSpace(base) == "" {
		return ""
	}
	sum := sha1.Sum([]byte(base))
	return fmt.Sprintf("%x", sum)
}

// approvalSummaryFromPrompt 从审批 prompt 中提取 command/cwd 摘要，避免点击后卡片继续占用大段空间。
func approvalSummaryFromPrompt(prompt string) string {
	if choiceCardKind(prompt) != cardKindApproval {
		return ""
	}
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(prompt), approvalPromptHead))
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "command: " + compactOneLine(raw, approvalSummaryMaxRune)
	}
	command := compactOneLine(firstStringValue(payload, "cmd", "command"), approvalSummaryMaxRune/2)
	cwd := compactOneLine(firstStringValue(payload, "cwd", "path"), approvalSummaryMaxRune/2)
	lines := make([]string, 0, 2)
	if command != "" {
		lines = append(lines, "command: "+command)
	}
	if cwd != "" {
		lines = append(lines, "cwd: "+cwd)
	}
	return compactOneLine(strings.Join(lines, "\n"), approvalSummaryMaxRune)
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

func compactOneLine(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
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
		Owner:      callbackValueString(value, approvalOwnerValueKey),
		Panel:      callbackValueString(value, "approval_panel") == "1",
		Conv:       callbackValueString(value, "conv"),
		SessionKey: callbackValueString(value, feishuSessionMetadataKey),
		UserID:     strings.TrimSpace(event.Event.Operator.OpenID),
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
