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

type feishuMentionCheck struct {
	NormalizedMentioned bool
	Mentions            []*larkim.MentionEvent
	Content             string
	AppID               string
}

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
	normalized := normalize.ParseMessage(event)
	mentionedBot := false
	content := ""
	if normalized != nil {
		mentionedBot = normalized.MentionedBot
		content = normalized.Content
	}
	return isMentionedBotFromParts(feishuMentionCheck{
		NormalizedMentioned: mentionedBot,
		Mentions:            feishuMessageMentions(event),
		Content:             content,
		AppID:               appID,
	})
}

// isMentionedBotFromParts 按可信度识别 @bot，避免把普通用户 @ 误判为 bot。
func isMentionedBotFromParts(check feishuMentionCheck) bool {
	if check.NormalizedMentioned {
		return true
	}
	if len(check.Mentions) == 0 {
		return false
	}
	appID := strings.TrimSpace(check.AppID)
	for _, mention := range check.Mentions {
		if mention == nil {
			continue
		}
		if appID != "" && strings.TrimSpace(valueFromUserID(mention.Id)) == appID {
			return true
		}
		if isBotMentionType(stringValue(mention.MentionedType)) && mentionKeyMatchesContent(mention, check.Content) {
			return true
		}
	}
	return false
}

// feishuMessageMentions 安全读取飞书消息里的 @ 列表。
func feishuMessageMentions(event *larkim.P2MessageReceiveV1) []*larkim.MentionEvent {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	return event.Event.Message.Mentions
}

// isBotMentionType 只接受飞书明确标记为应用或机器人的 @ 身份。
func isBotMentionType(mentionedType string) bool {
	switch strings.ToLower(strings.TrimSpace(mentionedType)) {
	case "app", "bot", "tenant_app", "application":
		return true
	default:
		return false
	}
}

// mentionKeyMatchesContent 用 mention key 约束兼容判断，避免无正文时错放行。
func mentionKeyMatchesContent(mention *larkim.MentionEvent, content string) bool {
	key := strings.TrimSpace(stringValue(mention.Key))
	content = strings.TrimSpace(content)
	return key != "" && content != "" && strings.Contains(content, key)
}

// valueFromUserID 返回飞书用户标识中的可用 ID。
func valueFromUserID(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
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
