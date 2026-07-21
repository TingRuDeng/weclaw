package feishu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const approvalPanelMaxSummaryRunes = 96

type approvalPanelItem struct {
	Key      string
	Summary  string
	Choices  []approvalPanelChoice
	Choice   string
	Label    string
	Status   string
	TaskCard string
}

type approvalPanelChoice struct {
	ID         string
	Label      string
	Owner      string
	Conv       string
	SessionKey string
}

type approvalPanelSnapshot struct {
	CardID string
	Seq    int
	Items  []approvalPanelItem
}

type approvalPanelRequest struct {
	Prompt   string
	Choices  []platform.Choice
	Conv     string
	TaskCard string
}

// newApprovalPanelItem 把 Codex 审批选择转换为面板行，普通 AskChoices 不进入该路径。
func newApprovalPanelItem(req approvalPanelRequest) (approvalPanelItem, bool) {
	options := choiceOptions(req.Prompt, req.Choices, req.Conv)
	if options.Kind != cardKindApproval || strings.TrimSpace(req.TaskCard) == "" {
		return approvalPanelItem{}, false
	}
	key := ""
	itemChoices := make([]approvalPanelChoice, 0, len(req.Choices))
	for _, choice := range req.Choices {
		id := strings.TrimSpace(choice.ID)
		label := strings.TrimSpace(choice.Label)
		if id == "" || label == "" {
			continue
		}
		if key == "" {
			key = firstNonEmpty(strings.TrimSpace(choice.Metadata["approval_key"]), approvalPayloadKey(options))
		}
		itemChoices = append(itemChoices, approvalPanelChoice{
			ID:         id,
			Label:      label,
			Owner:      strings.TrimSpace(choice.Metadata[approvalOwnerValueKey]),
			Conv:       req.Conv,
			SessionKey: strings.TrimSpace(choice.Metadata[feishuSessionMetadataKey]),
		})
	}
	if key == "" || len(itemChoices) == 0 {
		return approvalPanelItem{}, false
	}
	return approvalPanelItem{
		Key:      key,
		Summary:  compactOneLine(options.Summary, approvalPanelMaxSummaryRunes),
		Choices:  itemChoices,
		TaskCard: strings.TrimSpace(req.TaskCard),
	}, true
}

func buildApprovalPanelCardJSON(snapshot approvalPanelSnapshot) (string, error) {
	card := buildApprovalPanelCardData(snapshot)
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal feishu approval panel card: %w", err)
	}
	return string(data), nil
}

func buildApprovalPanelCallbackCard(snapshot approvalPanelSnapshot) *callback.Card {
	return &callback.Card{Type: "raw", Data: buildApprovalPanelCardData(snapshot)}
}

func buildApprovalPanelCardData(snapshot approvalPanelSnapshot) map[string]any {
	elements := make([]map[string]any, 0, len(snapshot.Items)*2+1)
	elements = append(elements, map[string]any{
		"tag":       "markdown",
		"content":   approvalPanelTitle(snapshot),
		"text_size": "normal",
	})
	for index, item := range snapshot.Items {
		elements = append(elements, approvalPanelItemElement(index, item))
		if item.Status == "" {
			elements = append(elements, approvalPanelButtons(item)...)
		}
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":     true,
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "WeClaw 审批",
			},
			"template": approvalPanelTemplate(snapshot),
		},
		"body": map[string]any{
			"direction": "vertical",
			"elements":  elements,
		},
	}
}

func approvalPanelTitle(snapshot approvalPanelSnapshot) string {
	pending, handled := approvalPanelCounts(snapshot.Items)
	warnings := approvalPanelWarningCount(snapshot.Items)
	if pending == 0 && handled == 0 {
		return "✅ 本轮审批已处理，记录已收纳到任务卡片。"
	}
	if pending == 0 {
		if warnings > 0 {
			return fmt.Sprintf("⚠️ 已处理审批：%d 个，其中 %d 个结果未确认或已过期", handled, warnings)
		}
		return fmt.Sprintf("✅ 已处理审批：%d 个", handled)
	}
	if warnings > 0 {
		return fmt.Sprintf("**待处理审批：%d 个**\n\n已处理：%d 个，其中 %d 个结果未确认或已过期", pending, handled, warnings)
	}
	return fmt.Sprintf("**待处理审批：%d 个**\n\n已处理：%d 个", pending, handled)
}

func approvalPanelTemplate(snapshot approvalPanelSnapshot) string {
	pending, _ := approvalPanelCounts(snapshot.Items)
	if pending == 0 {
		if approvalPanelWarningCount(snapshot.Items) > 0 {
			return "yellow"
		}
		return "green"
	}
	return "blue"
}

func approvalPanelWarningCount(items []approvalPanelItem) int {
	warnings := 0
	for _, item := range items {
		switch strings.TrimSpace(item.Status) {
		case approvalStatusExpired, approvalStatusUnconfirmed:
			warnings++
		}
	}
	return warnings
}

func approvalPanelCounts(items []approvalPanelItem) (int, int) {
	pending := 0
	handled := 0
	for _, item := range items {
		if item.Status == "" {
			pending++
		} else {
			handled++
		}
	}
	return pending, handled
}

func approvalPanelItemElement(index int, item approvalPanelItem) map[string]any {
	content := fmt.Sprintf("**%d. %s**", index+1, approvalPanelItemStatus(item))
	if item.Summary != "" {
		content += "\n" + item.Summary
	}
	return map[string]any{
		"tag":       "markdown",
		"content":   content,
		"text_size": "normal",
	}
}

func approvalPanelItemStatus(item approvalPanelItem) string {
	if item.Status == "" {
		return "待审批"
	}
	status, _ := approvalHandledStatus(parsedCardAction{
		Choice: item.Choice,
		Label:  item.Label,
		Status: item.Status,
	})
	label := strings.TrimSpace(firstNonEmpty(item.Label, item.Choice))
	if label == "" {
		return status
	}
	return status + "：" + label
}

func approvalPanelButtons(item approvalPanelItem) []map[string]any {
	buttons := make([]map[string]any, 0, len(item.Choices))
	for _, choice := range item.Choices {
		value := map[string]string{
			"action":         cardActionChoice,
			"choice":         choice.ID,
			"conv":           choice.Conv,
			"label":          choice.Label,
			"kind":           cardKindApproval,
			"approval_key":   item.Key,
			"task_card_id":   item.TaskCard,
			"approval_panel": "1",
		}
		if item.Summary != "" {
			value["summary"] = item.Summary
		}
		if choice.SessionKey != "" {
			value[feishuSessionMetadataKey] = choice.SessionKey
		}
		if choice.Owner != "" {
			value[approvalOwnerValueKey] = choice.Owner
		}
		buttons = append(buttons, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": choice.Label,
			},
			"type":  approvalPanelButtonType(choice),
			"value": value,
		})
	}
	return buttons
}

func approvalPanelButtonType(choice approvalPanelChoice) string {
	label := strings.ToLower(strings.TrimSpace(choice.Label + " " + choice.ID))
	if strings.Contains(label, "cancel") || strings.Contains(label, "deny") || strings.Contains(label, "拒") {
		return "default"
	}
	return "primary"
}
