package messaging

import (
	"regexp"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

var numberedChoiceLinePattern = regexp.MustCompile(`^\s*(\d+)[\.\)、）)]\s*(.+?)\s*$`)
var englishChoicePromptPattern = regexp.MustCompile(`\b(choose|select)\b`)

type choiceDetection struct {
	CleanText string
	Prompt    string
	Choices   []platform.Choice
}

// detectChoices 从 agent 最终回复里识别编号选项，只有出现选择提示词时才触发。
func detectChoices(reply string) (choiceDetection, bool) {
	if !containsChoicePrompt(reply) {
		return choiceDetection{}, false
	}
	lines := strings.Split(reply, "\n")
	choices := make([]platform.Choice, 0)
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if match := numberedChoiceLinePattern.FindStringSubmatch(line); match != nil {
			choices = append(choices, platform.Choice{ID: match[1], Label: strings.TrimSpace(match[2])})
			continue
		}
		cleanLines = append(cleanLines, line)
	}
	if len(choices) < 2 {
		return choiceDetection{}, false
	}
	cleanText := strings.TrimSpace(strings.Join(cleanLines, "\n"))
	prompt := lastNonEmptyLine(cleanLines)
	if prompt == "" {
		prompt = "请选择一个选项"
	}
	return choiceDetection{CleanText: cleanText, Prompt: prompt, Choices: choices}, true
}

// containsChoicePrompt 判断回复是否真的在请求用户选择，避免误判普通编号列表。
func containsChoicePrompt(text string) bool {
	normalized := strings.ToLower(text)
	keywords := []string{"请选择", "请回复", "回复编号", "输入编号", "选择编号", "选一个"}
	for _, keyword := range keywords {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return englishChoicePromptPattern.MatchString(normalized)
}

// lastNonEmptyLine 返回最后一行非空文本作为按钮 prompt。
func lastNonEmptyLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
