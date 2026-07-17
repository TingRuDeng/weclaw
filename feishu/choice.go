package feishu

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
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
	approvalPromptMarker   = "请求执行敏感操作，请确认："
	approvalSummaryMaxRune = 160
	modelSettingAgentKey   = "model_setting_agent"
	cardRevisionValueKey   = "card_revision"
)

type parsedCardAction struct {
	Action             string
	Choice             string
	Kind               string
	Label              string
	Summary            string
	TaskCard           string
	Approval           string
	Owner              string
	Panel              bool
	Conv               string
	SessionKey         string
	AgentName          string
	UserID             string
	UserAliases        []string
	ChatID             string
	MessageID          string
	EventID            string
	CardRevision       string
	NavigationSnapshot string
	Status             string
}

type choiceButtonOptions struct {
	ConversationKey string
	Kind            string
	AgentName       string
	Summary         string
	Revision        string
}

// buildChoiceCard 构建飞书按钮卡片，每个按钮携带可回放到业务层的动作值。
func buildChoiceCard(prompt string, choices []platform.Choice, conversationKey string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "请选择："
	}
	options := choiceOptions(prompt, choices, conversationKey)
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
	headerTitle := "WeClaw"
	if options.AgentName != "" {
		switch options.Kind {
		case cardKindApproval:
			headerTitle = options.AgentName + " 授权"
		case platform.ChoiceInteractionUserInput:
			headerTitle = options.AgentName + " 提问"
		}
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
				"content": headerTitle,
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

func choiceOptions(prompt string, choices []platform.Choice, conversationKey string) choiceButtonOptions {
	options := choiceButtonOptions{ConversationKey: conversationKey, Revision: uuid.NewString()}
	for _, choice := range choices {
		if options.Kind == "" {
			options.Kind = strings.TrimSpace(choice.Metadata[platform.ChoiceMetadataInteractionKind])
		}
		if options.AgentName == "" {
			options.AgentName = strings.TrimSpace(choice.Metadata[platform.ChoiceMetadataAgentName])
		}
	}
	if options.Kind == "" {
		options.Kind = choiceCardKind(prompt)
	}
	if options.Kind == cardKindApproval {
		options.Summary = approvalSummaryFromPrompt(prompt)
	}
	return options
}

// buildChoiceButtons 过滤无效选项，并生成 CardKit 2.0 可点击按钮元素。
func buildChoiceButtons(choices []platform.Choice, options choiceButtonOptions) []map[string]any {
	buttons := make([]map[string]any, 0, len(choices)+1)
	navigationStarted := false
	for _, choice := range choices {
		id := strings.TrimSpace(choice.ID)
		label := strings.TrimSpace(choice.Label)
		if id == "" || label == "" {
			continue
		}
		if choice.Metadata[platform.ChoiceMetadataSection] == platform.ChoiceSectionNavigation && !navigationStarted {
			buttons = append(buttons, map[string]any{"tag": "hr"})
			navigationStarted = true
		}
		value := map[string]string{
			"action":             cardActionChoice,
			"choice":             id,
			"conv":               options.ConversationKey,
			"label":              label,
			cardRevisionValueKey: options.Revision,
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
		if agentName := strings.TrimSpace(choice.Metadata[modelSettingAgentKey]); agentName != "" {
			value[modelSettingAgentKey] = agentName
		}
		if snapshot := strings.TrimSpace(choice.Metadata[platform.ChoiceMetadataNavigationSnapshot]); snapshot != "" {
			value[platform.ChoiceMetadataNavigationSnapshot] = snapshot
		}
		buttonType := "primary"
		if choice.Metadata[platform.ChoiceMetadataButtonType] == platform.ChoiceButtonTypeDefault {
			buttonType = platform.ChoiceButtonTypeDefault
		}
		buttons = append(buttons, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": label,
			},
			"type":  buttonType,
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
	raw := strings.TrimSpace(prompt)
	if marker := strings.Index(raw, approvalPromptMarker); marker >= 0 {
		raw = raw[marker+len(approvalPromptMarker):]
	} else {
		raw = strings.TrimPrefix(raw, approvalPromptHead)
	}
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

// choiceCardKind 只标记 Agent 授权卡片，避免普通导航/选择卡片点击后被改成授权状态。
func choiceCardKind(prompt string) string {
	if strings.Contains(strings.TrimSpace(prompt), approvalPromptMarker) {
		return cardKindApproval
	}
	return ""
}
