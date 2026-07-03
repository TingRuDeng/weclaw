package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// feishuChoiceSessionMetadata 仅在原飞书消息已带 session key 时，给按钮回调透传同一会话。
func feishuChoiceSessionMetadata(msg platform.IncomingMessage, routeUserID string) map[string]string {
	if msg.Platform != platform.PlatformFeishu || platformMessageSessionKey(msg) == "" {
		return nil
	}
	routeUserID = strings.TrimSpace(routeUserID)
	if routeUserID == "" || !strings.HasPrefix(routeUserID, string(platform.PlatformFeishu)+":") {
		return nil
	}
	return map[string]string{feishuSessionMetadataKey: routeUserID}
}

// feishuSessionKeyFromRoute 只把飞书会话路由写回飞书按钮，避免裸用户 ID 误走历史微信兼容会话。
func feishuSessionKeyFromRoute(routeUserID string) string {
	routeUserID = strings.TrimSpace(routeUserID)
	if strings.HasPrefix(routeUserID, string(platform.PlatformFeishu)+":") {
		return routeUserID
	}
	return ""
}

// platformChoicesWithMetadata 返回新切片，避免复用调用方 choice 时污染原始对象。
func platformChoicesWithMetadata(choices []platform.Choice, metadata map[string]string) []platform.Choice {
	if len(metadata) == 0 {
		return choices
	}
	result := make([]platform.Choice, 0, len(choices))
	for _, choice := range choices {
		choice.Metadata = mergeChoiceMetadata(choice.Metadata, metadata)
		result = append(result, choice)
	}
	return result
}

// mergeChoiceMetadata 以后传 metadata 为准，保证 route key 不被旧按钮值覆盖。
func mergeChoiceMetadata(base map[string]string, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}
