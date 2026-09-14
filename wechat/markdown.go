package wechat

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	reCodeBlock  = regexp.MustCompile("(?s)```[^\n]*\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reImage      = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]*)\)`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*)\)`)
	reTableSep   = regexp.MustCompile(`(?m)^\|[\s:|\-]+\|$`)
	reTableRow   = regexp.MustCompile(`(?m)^\|(.+)\|$`)
	reHeader     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reBlockquote = regexp.MustCompile(`(?m)^>\s?`)
	reHR         = regexp.MustCompile(`(?m)^[-*_]{3,}\s*$`)
	reUL         = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
)

// MarkdownToPlainText 将 markdown 转成适合微信展示的纯文本。
func MarkdownToPlainText(text string) string {
	protected := markdownProtector{}
	result := reCodeBlock.ReplaceAllStringFunc(text, func(match string) string {
		parts := reCodeBlock.FindStringSubmatch(match)
		if len(parts) > 1 {
			return protected.put(strings.TrimSpace(parts[1]))
		}
		return match
	})
	result = reImage.ReplaceAllStringFunc(result, func(match string) string {
		parts := reImage.FindStringSubmatch(match)
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			return "[图片]"
		}
		return "[图片: " + strings.TrimSpace(parts[1]) + "]"
	})
	result = reLink.ReplaceAllString(result, "$1 ($2)")
	result = reTableSep.ReplaceAllString(result, "")
	result = reTableRow.ReplaceAllStringFunc(result, func(match string) string {
		parts := reTableRow.FindStringSubmatch(match)
		if len(parts) <= 1 {
			return match
		}
		cells := strings.Split(parts[1], "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		return strings.Join(cells, "  ")
	})
	result = reHeader.ReplaceAllString(result, "")
	result = reBold.ReplaceAllStringFunc(result, func(match string) string {
		parts := reBold.FindStringSubmatch(match)
		if parts[1] != "" {
			return parts[1]
		}
		return parts[2]
	})
	result = reStrike.ReplaceAllString(result, "$1")
	result = reBlockquote.ReplaceAllString(result, "")
	result = reHR.ReplaceAllString(result, "")
	result = reUL.ReplaceAllString(result, "${1}• ")
	result = reInlineCode.ReplaceAllStringFunc(result, func(match string) string {
		parts := reInlineCode.FindStringSubmatch(match)
		if len(parts) > 1 {
			return protected.put(parts[1])
		}
		return match
	})
	result = reBlankLines.ReplaceAllString(result, "\n\n")
	result = strings.TrimSpace(result)
	return protected.restore(result)
}

type markdownProtector struct {
	values []string
}

func (p *markdownProtector) put(value string) string {
	token := fmt.Sprintf("\x00WECLAW_MD_%d\x00", len(p.values))
	p.values = append(p.values, value)
	return token
}

func (p *markdownProtector) restore(text string) string {
	for index, value := range p.values {
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00WECLAW_MD_%d\x00", index), value)
	}
	return text
}

// FormatTextForWeChatDisplay 将逻辑换行转换成微信气泡稳定展示的空行分隔。
func FormatTextForWeChatDisplay(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts))
	for _, line := range parts {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n\n")
}
