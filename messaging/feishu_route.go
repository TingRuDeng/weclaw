package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// isLegacyFeishuGroupRouteKey 仅用于兼容未显式携带 RouteIsGroup 的旧持久化状态或旧入站测试消息。
func isLegacyFeishuGroupRouteKey(routeUserID string) bool {
	normalized := ":" + strings.ToLower(strings.TrimSpace(routeUserID)) + ":"
	return strings.Contains(normalized, ":group:")
}

// platformMessageIsGroup 优先读取一等结构化 Route.IsGroup，仅兼容旧调用或历史测试未填充时回退判断。
func platformMessageIsGroup(msg platform.IncomingMessage, routeUserID string) bool {
	if msg.Route.IsGroup {
		return true
	}
	if msg.Platform != platform.PlatformFeishu {
		return false
	}
	if msg.Metadata != nil {
		switch strings.ToLower(strings.TrimSpace(msg.Metadata["feishu_chat_type"])) {
		case "group", "topic_group":
			return true
		case "p2p", "private", "direct":
			return false
		}
	}
	return isLegacyFeishuGroupRouteKey(routeUserID)
}

// platformMessageChoiceRoute 返回应回传给按钮选择卡的一等结构化路由。
func platformMessageChoiceRoute(msg platform.IncomingMessage, routeUserID string) platform.SessionRoute {
	if msg.Platform != platform.PlatformFeishu || platformMessageSessionKey(msg) == "" {
		return platform.SessionRoute{}
	}
	routeUserID = strings.TrimSpace(routeUserID)
	if routeUserID == "" || !strings.HasPrefix(routeUserID, string(platform.PlatformFeishu)+":") {
		return platform.SessionRoute{}
	}
	return platform.SessionRoute{
		Key:     routeUserID,
		IsGroup: platformMessageIsGroup(msg, routeUserID),
	}
}

// choiceSessionRouteForInteraction 为审批与问答选项构建一等结构化路由。
func choiceSessionRouteForInteraction(routeUserID string, routeIsGroup bool) platform.SessionRoute {
	routeUserID = strings.TrimSpace(routeUserID)
	if !strings.HasPrefix(routeUserID, string(platform.PlatformFeishu)+":") {
		return platform.SessionRoute{IsGroup: routeIsGroup}
	}
	return platform.SessionRoute{Key: routeUserID, IsGroup: routeIsGroup}
}

// platformChoicesWithRoute 返回新切片，避免复用调用方 choice 时污染原始对象。
func platformChoicesWithRoute(choices []platform.Choice, route platform.SessionRoute) []platform.Choice {
	route.Key = strings.TrimSpace(route.Key)
	if route.Key == "" && !route.IsGroup {
		return choices
	}
	result := make([]platform.Choice, 0, len(choices))
	for _, choice := range choices {
		choice.Route = route
		result = append(result, choice)
	}
	return result
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

// mergeChoiceMetadata 以后传 metadata 为准，保证新字段不被旧按钮值覆盖。
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
