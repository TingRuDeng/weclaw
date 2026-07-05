package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

func platformMessageText(msg platform.IncomingMessage) string {
	if msg.RawCommand != nil && msg.RawCommand.Action == "choice" {
		return strings.TrimSpace(msg.RawCommand.Value["choice"])
	}
	return msg.Text
}

// platformMessageSessionKey 返回平台 adapter 明确传入的会话 key。
func platformMessageSessionKey(msg platform.IncomingMessage) string {
	if msg.Platform == platform.PlatformFeishu && msg.Metadata != nil {
		return strings.TrimSpace(msg.Metadata[feishuSessionMetadataKey])
	}
	return ""
}

// platformMessageRouteUserID 返回 agent 会话路由使用的用户维度，不改变真实发送者 ID。
func platformMessageRouteUserID(msg platform.IncomingMessage) string {
	if sessionKey := platformMessageSessionKey(msg); sessionKey != "" {
		return sessionKey
	}
	return msg.UserID
}

func platformMessageDedupKey(msg platform.IncomingMessage) string {
	return strings.TrimSpace(string(msg.Platform)) + "\x00" + strings.TrimSpace(msg.AccountID) + "\x00" + strings.TrimSpace(msg.MessageID)
}
