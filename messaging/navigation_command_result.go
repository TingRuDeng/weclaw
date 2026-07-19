package messaging

import "github.com/fastclaw-ai/weclaw/platform"

// navigationCommandResult 显式区分命令文本与是否允许展示导航卡片。
type navigationCommandResult struct {
	Reply    string
	ShowCard bool
	Prompt   string
	Choices  []platform.Choice
}

// textNavigationResult 返回仅发送文本的命令结果。
func textNavigationResult(reply string) navigationCommandResult {
	return navigationCommandResult{Reply: reply}
}

// cardNavigationResult 返回可由平台升级为导航卡片的成功结果。
func cardNavigationResult(reply string) navigationCommandResult {
	return navigationCommandResult{Reply: reply, ShowCard: true}
}

// choiceNavigationResult 返回已经完成权限与 revision 校验的结构化选择卡。
func choiceNavigationResult(reply string, prompt string, choices []platform.Choice) navigationCommandResult {
	return navigationCommandResult{Reply: reply, Prompt: prompt, Choices: choices}
}
