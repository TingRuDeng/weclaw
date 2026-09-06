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
	content := rewriteFeishuLocalMarkdownLinks(opts.Content)
	if strings.TrimSpace(content) == "" {
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
			PreserveWhitespace: true,
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
	var chunks []string
	var fence resultMarkdownFence
	current := ""
	hasContent := false
	flush := func() {
		if hasContent {
			chunks = append(chunks, fence.close(current))
		}
		current = ""
		if fence.marker != "" {
			current = fence.opener + "\n"
		}
		hasContent = false
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		if line == "" {
			continue
		}
		next, boundary := fence.afterLine(line)
		for line != "" {
			fits, err := resultCardContentFits(sizingTitle, status, next.close(current+line))
			if err != nil {
				return nil, err
			}
			if fits {
				current += line
				hasContent = true
				fence = next
				break
			}
			if hasContent {
				flush()
				continue
			}
			// A fence delimiter cannot be split without changing Markdown semantics.
			if boundary {
				return nil, fmt.Errorf("result card cannot fit code fence delimiter")
			}
			runes := []rune(line)
			low, high, best := 1, len(runes), 0
			for low <= high {
				middle := low + (high-low)/2
				fits, err := resultCardContentFits(sizingTitle, status, fence.close(current+string(runes[:middle])))
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
			current += string(runes[:best])
			hasContent = true
			line = string(runes[best:])
			flush()
		}
	}
	flush()
	if len(chunks) == 0 {
		chunks = append(chunks, statusDefaultContent(status))
	}
	return chunks, nil
}

type resultMarkdownFence struct{ marker, opener string }

func (f resultMarkdownFence) close(content string) string {
	if f.marker == "" {
		return content
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + f.marker
}

func (f resultMarkdownFence) afterLine(line string) (resultMarkdownFence, bool) {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return f, false
	}
	n := 1
	for n < len(trimmed) && trimmed[n] == trimmed[0] {
		n++
	}
	if n < 3 {
		return f, false
	}
	if f.marker != "" {
		if trimmed[0] == f.marker[0] && n >= len(f.marker) && strings.TrimSpace(trimmed[n:]) == "" {
			return resultMarkdownFence{}, true
		}
		return f, false
	}
	if trimmed[0] == '`' && strings.Contains(trimmed[n:], "`") {
		return f, false
	}
	return resultMarkdownFence{marker: trimmed[:n], opener: line}, true
}

func resultCardContentFits(title string, status string, content string) (bool, error) {
	raw, err := buildCardV2(cardOptions{Status: status, Title: title, Content: content, PreserveWhitespace: true})
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
