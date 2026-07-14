package messaging

// navigationCommandResult 显式区分命令文本与是否允许展示导航卡片。
type navigationCommandResult struct {
	Reply    string
	ShowCard bool
}

// textNavigationResult 返回仅发送文本的命令结果。
func textNavigationResult(reply string) navigationCommandResult {
	return navigationCommandResult{Reply: reply}
}

// cardNavigationResult 返回可由平台升级为导航卡片的成功结果。
func cardNavigationResult(reply string) navigationCommandResult {
	return navigationCommandResult{Reply: reply, ShowCard: true}
}
