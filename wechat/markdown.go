package wechat

import (
	"regexp"
	"strings"
)

var (
	reCodeBlock  = regexp.MustCompile("(?s)```[^\n]*\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reImage      = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
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
	result := reCodeBlock.ReplaceAllStringFunc(text, func(match string) string {
		parts := reCodeBlock.FindStringSubmatch(match)
		if len(parts) > 1 {
			return strings.TrimSpace(parts[1])
		}
		return match
	})
	result = reImage.ReplaceAllString(result, "")
	result = reLink.ReplaceAllString(result, "$1")
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
	result = reInlineCode.ReplaceAllString(result, "$1")
	result = reBlankLines.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
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
