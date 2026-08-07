package feishu

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 飞书发送消息接口对卡片请求体限制为 30 KB；这里按完整消息 JSON 使用更保守的软上限。
const feishuResultMessageJSONSoftLimitBytes = 24 * 1024

// 接收目标位于完整 create-message 请求体中；预检时使用大于当前飞书 ID 的保守占位长度。
const feishuResultMessageReceiveIDReserveBytes = 128

const feishuResultCardTitleMaxRunes = 60

var feishuLocalMarkdownLinkPattern = regexp.MustCompile(`\[([^\]\n]+)\]\(<?(/[^)>\n]+)>?\)`)

type resultCardOptions struct {
	Title   string
	Status  string
	Content string
}

// buildResultCards 把一个逻辑终态结果渲染为一组有序静态卡片；每张卡都在本地完成容量预检。
func buildResultCards(opts resultCardOptions) ([]string, error) {
	status := resultCardStatus(opts.Status)
	content := rewriteFeishuLocalMarkdownLinks(strings.TrimSpace(opts.Content))
	if content == "" {
		content = statusDefaultContent(status)
	}
	baseTitle := compactResultCardTitle(opts.Title)
	chunks, err := splitResultCardMarkdown(baseTitle, status, content)
	if err != nil {
		return nil, err
	}
	cards := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		raw, err := buildCardV2(cardOptions{
			Status: status, Title: resultCardTitle(baseTitle, index+1, len(chunks)), Content: chunk,
		})
		if err != nil {
			return nil, err
		}
		messageSize, err := resultCardMessageJSONSize(raw)
		if err != nil {
			return nil, err
		}
		if messageSize > feishuResultMessageJSONSoftLimitBytes {
			return nil, fmt.Errorf("result message payload exceeds soft limit: rendered=%d soft_limit=%d", messageSize, feishuResultMessageJSONSoftLimitBytes)
		}
		cards = append(cards, raw)
	}
	return cards, nil
}

func resultCardStatus(status string) string {
	switch status {
	case cardStatusError, cardStatusStopped:
		return status
	default:
		return cardStatusDone
	}
}

func compactResultCardTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "WeClaw"
	}
	runes := []rune(title)
	if len(runes) > feishuResultCardTitleMaxRunes {
		title = string(runes[:feishuResultCardTitleMaxRunes]) + "…"
	}
	return title
}

func resultCardTitle(base string, index int, total int) string {
	title := base + " · 最终结果"
	if total > 1 {
		title += fmt.Sprintf(" · %d/%d", index, total)
	}
	return title
}

func rewriteFeishuLocalMarkdownLinks(content string) string {
	return feishuLocalMarkdownLinkPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := feishuLocalMarkdownLinkPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		label := strings.TrimSpace(parts[1])
		path := strings.ReplaceAll(strings.TrimSpace(parts[2]), "`", "ˋ")
		return label + "（`" + path + "`）"
	})
}

func splitResultCardMarkdown(title string, status string, content string) ([]string, error) {
	sizingTitle := resultCardTitle(title, 999999, 999999)
	lines := strings.Split(content, "\n")
	chunks := make([]string, 0, 1)
	current := ""
	flush := func() {
		if chunk := strings.TrimSpace(current); chunk != "" {
			chunks = append(chunks, chunk)
		}
		current = ""
	}
	for _, line := range lines {
		candidate := line
		if current != "" {
			candidate = current + "\n" + line
		}
		fits, err := resultCardContentFits(sizingTitle, status, candidate)
		if err != nil {
			return nil, err
		}
		if fits {
			current = candidate
			continue
		}
		flush()
		fits, err = resultCardContentFits(sizingTitle, status, line)
		if err != nil {
			return nil, err
		}
		if fits {
			current = line
			continue
		}
		parts, err := splitOversizedResultCardLine(sizingTitle, status, line)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, parts...)
	}
	flush()
	if len(chunks) == 0 {
		chunks = append(chunks, statusDefaultContent(status))
	}
	return chunks, nil
}

func splitOversizedResultCardLine(title string, status string, line string) ([]string, error) {
	runes := []rune(line)
	parts := make([]string, 0, 2)
	for len(runes) > 0 {
		low, high := 1, len(runes)
		best := 0
		for low <= high {
			middle := low + (high-low)/2
			fits, err := resultCardContentFits(title, status, string(runes[:middle]))
			if err != nil {
				return nil, err
			}
			if fits {
				best = middle
				low = middle + 1
			} else {
				high = middle - 1
			}
		}
		if best == 0 {
			return nil, fmt.Errorf("result card cannot fit one content rune")
		}
		parts = append(parts, string(runes[:best]))
		runes = runes[best:]
	}
	return parts, nil
}

func resultCardContentFits(title string, status string, content string) (bool, error) {
	raw, err := buildCardV2(cardOptions{Status: status, Title: title, Content: content})
	if err != nil {
		return false, err
	}
	size, err := resultCardMessageJSONSize(raw)
	if err != nil {
		return false, err
	}
	return size <= feishuResultMessageJSONSoftLimitBytes, nil
}

func resultCardMessageJSONSize(cardJSON string) (int, error) {
	payload, err := json.Marshal(map[string]any{
		"receive_id": strings.Repeat("x", feishuResultMessageReceiveIDReserveBytes),
		"msg_type":   "interactive",
		"content":    cardJSON,
		"uuid":       strings.Repeat("x", 36),
	})
	if err != nil {
		return 0, fmt.Errorf("marshal feishu result message envelope: %w", err)
	}
	return len(payload), nil
}
