package messaging

import "github.com/fastclaw-ai/weclaw/platform"

// navigationCommandResult 显式区分命令文本与是否允许展示导航卡片。
type navigationCommandResult struct {
	Reply           string
	ShowCard        bool
	StatusCardTitle string
	Prompt          string
	Choices         []platform.Choice
}

// textNavigationResult 返回仅发送文本的命令结果。
func textNavigationResult(reply string) navigationCommandResult {
	return navigationCommandResult{Reply: reply}
}

// cardNavigationResult 返回可由平台升级为导航卡片的成功结果。
func cardNavigationResult(reply string) navigationCommandResult {
	return navigationCommandResult{Reply: reply, ShowCard: true}
}

// statusCardNavigationResult 返回可由支持卡片的平台升级为完成状态卡的成功结果。
func statusCardNavigationResult(reply string, title string) navigationCommandResult {
	return navigationCommandResult{Reply: reply, StatusCardTitle: title}
}

// choiceNavigationResult 返回已经完成权限与 revision 校验的结构化选择卡。
func choiceNavigationResult(reply string, prompt string, choices []platform.Choice) navigationCommandResult {
	return navigationCommandResult{Reply: reply, Prompt: prompt, Choices: choices}
}
