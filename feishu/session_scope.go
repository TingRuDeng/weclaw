package feishu

import (
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	feishuSessionMetadataKey = "feishu_session_key"
	feishuMentionMetadataKey = "feishu_is_mentioned"
	feishuScopePrefix        = "feishu"
)

// FeishuSessionScope 描述飞书消息进入 agent 前需要固定的会话维度。
type FeishuSessionScope struct {
	TenantID     string
	ChatID       string
	ThreadID     string
	RootID       string
	MessageID    string
	SenderOpenID string
	ChatType     string
	IsMentioned  bool
	AgentName    string
}

// FeishuSessionOptions 控制飞书群聊触发和 thread 会话隔离策略。
type FeishuSessionOptions struct {
	RequireMentionInGroup bool
	ThreadIsolation       bool
}

// DefaultFeishuSessionOptions 返回安全默认值：群聊必须 @，thread 默认隔离。
func DefaultFeishuSessionOptions() FeishuSessionOptions {
	return FeishuSessionOptions{RequireMentionInGroup: true, ThreadIsolation: true}
}

// ExtractFeishuSessionScope 从飞书事件中抽取统一会话语义。
func ExtractFeishuSessionScope(event *larkim.P2MessageReceiveV1) FeishuSessionScope {
	scope := FeishuSessionScope{}
	if event == nil {
		return scope
	}
	scope.TenantID = strings.TrimSpace(event.TenantKey())
	if event.Event == nil {
		return scope
	}
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil && event.Event.Sender.SenderId.OpenId != nil {
		scope.SenderOpenID = strings.TrimSpace(*event.Event.Sender.SenderId.OpenId)
	}
	if event.Event.Message == nil {
		return scope
	}
	msg := event.Event.Message
	scope.ChatID = stringValue(msg.ChatId)
	scope.ThreadID = stringValue(msg.ThreadId)
	scope.RootID = stringValue(msg.RootId)
	scope.MessageID = stringValue(msg.MessageId)
	scope.ChatType = stringValue(msg.ChatType)
	scope.IsMentioned = hasAnyMention(msg)
	return scope
}

// ResolveThreadKey 按 root_id > thread_id > message_id 的优先级确定 thread key。
func ResolveThreadKey(scope FeishuSessionScope) string {
	if value := strings.TrimSpace(scope.RootID); value != "" {
		return value
	}
	if value := strings.TrimSpace(scope.ThreadID); value != "" {
		return value
	}
	return strings.TrimSpace(scope.MessageID)
}

// BuildFeishuSessionKey 根据飞书会话范围生成稳定 session key。
func BuildFeishuSessionKey(scope FeishuSessionScope, threadIsolation bool) string {
	parts := []string{feishuScopePrefix}
	if tenant := strings.TrimSpace(scope.TenantID); tenant != "" {
		parts = append(parts, tenant)
	}
	if isFeishuGroupChat(scope.ChatType) {
		parts = append(parts, "group", strings.TrimSpace(scope.ChatID))
		if threadIsolation && hasThreadFields(scope) {
			parts = append(parts, ResolveThreadKey(scope))
		}
		return strings.Join(parts, ":")
	}
	parts = append(parts, "dm", strings.TrimSpace(scope.ChatID), strings.TrimSpace(scope.SenderOpenID))
	return strings.Join(parts, ":")
}

// isFeishuGroupChat 判断飞书事件是否属于群或话题群。
func isFeishuGroupChat(chatType string) bool {
	chatType = strings.TrimSpace(chatType)
	return chatType == "group" || chatType == "topic_group"
}

// hasThreadFields 判断事件是否携带真实 thread/root 维度。
func hasThreadFields(scope FeishuSessionScope) bool {
	return strings.TrimSpace(scope.RootID) != "" || strings.TrimSpace(scope.ThreadID) != ""
}

// hasAnyMention 保留 extractor 层面的原始 @ 信息。
func hasAnyMention(msg *larkim.EventMessage) bool {
	return msg != nil && len(msg.Mentions) > 0
}

// isMentionedBot 判断消息是否明确 @ 当前机器人。
func isMentionedBot(event *larkim.P2MessageReceiveV1, appID string) bool {
	if normalized := normalize.ParseMessage(event); normalized != nil && normalized.MentionedBot {
		return true
	}
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return false
	}
	appID = strings.TrimSpace(appID)
	for _, mention := range event.Event.Message.Mentions {
		if mention == nil || mention.Id == nil {
			continue
		}
		if strings.TrimSpace(valueFromUserID(mention.Id)) == appID {
			return true
		}
	}
	return false
}

// valueFromUserID 返回飞书用户标识中的可用 ID。
func valueFromUserID(id *larkim.UserId) string {
	if id.OpenId != nil {
		return *id.OpenId
	}
	if id.UserId != nil {
		return *id.UserId
	}
	return ""
}

// stringValue 安全读取飞书 SDK 的字符串指针字段。
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
