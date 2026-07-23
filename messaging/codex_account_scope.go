package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// isPrivatePlatformMessage 只把可证明为私聊的窗口用于主机级控制命令。
// 飞书卡片回调不再携带 chat_type，因此必须从 adapter 固定的 route key 恢复范围。
func isPrivatePlatformMessage(msg platform.IncomingMessage, routeUserID string) bool {
	if msg.Platform != platform.PlatformFeishu {
		return true
	}
	chatType := ""
	if msg.Metadata != nil {
		chatType = strings.ToLower(strings.TrimSpace(msg.Metadata["feishu_chat_type"]))
	}
	switch chatType {
	case "group", "topic_group":
		return false
	case "p2p", "private", "direct":
		return true
	}
	route := ":" + strings.ToLower(strings.TrimSpace(routeUserID)) + ":"
	if strings.Contains(route, ":group:") {
		return false
	}
	return strings.Contains(route, ":dm:")
}

func isPrivateCodexCommandMessage(msg platform.IncomingMessage, routeUserID string) bool {
	return isPrivatePlatformMessage(msg, routeUserID)
}
