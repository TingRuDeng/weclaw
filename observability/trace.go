package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	traceSummaryMaxRunes = 500
	traceRouteHashBytes  = 12
)

// TraceContext 是一次入站消息及其派生任务的不可变关联信息。
// RouteKey 仅在进程内传播，持久化时只记录不可逆哈希。
type TraceContext struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

	Platform  string `json:"platform,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	ChatID    string `json:"chat_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	RouteKey  string `json:"-"`

	AgentName      string `json:"agent_name,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	CardID         string `json:"card_id,omitempty"`
	ReplyID        string `json:"reply_id,omitempty"`
}

// TraceSeed 描述一条入站消息可安全进入 Trace 的稳定标识。
type TraceSeed struct {
	Platform  string
	AccountID string
	ChatID    string
	MessageID string
	RouteKey  string
}

// NewTraceContext 为一条入站消息生成新的根 Trace。
func NewTraceContext(seed TraceSeed) TraceContext {
	return TraceContext{
		TraceID: uuid.NewString(), SpanID: uuid.NewString(),
		Platform: strings.TrimSpace(seed.Platform), AccountID: strings.TrimSpace(seed.AccountID),
		ChatID: strings.TrimSpace(seed.ChatID), MessageID: strings.TrimSpace(seed.MessageID),
		RouteKey: strings.TrimSpace(seed.RouteKey),
	}
}

// Ensure 确保直接调用业务层的测试或本地入口也具备稳定 Trace/Span ID。
func (trace TraceContext) Ensure() TraceContext {
	if strings.TrimSpace(trace.TraceID) == "" {
		trace.TraceID = uuid.NewString()
	}
	if strings.TrimSpace(trace.SpanID) == "" {
		trace.SpanID = uuid.NewString()
	}
	return trace
}

// Branch 为广播 Agent 或排队续跑创建同根 Trace 的子 Span。
func (trace TraceContext) Branch(agentName string) TraceContext {
	trace = trace.Ensure()
	trace.ParentSpanID = trace.SpanID
	trace.SpanID = uuid.NewString()
	trace.AgentName = strings.TrimSpace(agentName)
	trace.TaskID = ""
	trace.ConversationID = ""
	trace.SessionID = ""
	trace.ThreadID = ""
	trace.TurnID = ""
	trace.CardID = ""
	trace.ReplyID = ""
	return trace
}

func (trace TraceContext) WithClientID(clientID string) TraceContext {
	trace.ClientID = strings.TrimSpace(clientID)
	return trace.Ensure()
}

func (trace TraceContext) WithAgent(agentName string) TraceContext {
	trace.AgentName = strings.TrimSpace(agentName)
	return trace.Ensure()
}

func (trace TraceContext) WithTask(taskID string) TraceContext {
	trace.TaskID = strings.TrimSpace(taskID)
	return trace.Ensure()
}

func (trace TraceContext) WithConversation(conversationID string) TraceContext {
	trace.ConversationID = strings.TrimSpace(conversationID)
	return trace.Ensure()
}

func (trace TraceContext) WithSession(sessionID string) TraceContext {
	trace.SessionID = strings.TrimSpace(sessionID)
	return trace.Ensure()
}

func (trace TraceContext) WithThreadTurn(threadID string, turnID string) TraceContext {
	trace.ThreadID = strings.TrimSpace(threadID)
	trace.TurnID = strings.TrimSpace(turnID)
	return trace.Ensure()
}

func (trace TraceContext) WithDelivery(cardID string, replyID string) TraceContext {
	trace.CardID = strings.TrimSpace(cardID)
	trace.ReplyID = strings.TrimSpace(replyID)
	return trace.Ensure()
}

type traceContextKey struct{}

// ContextWithTrace 通过 context 传播 Trace；排队和 outbox 仍应显式保存副本。
func ContextWithTrace(ctx context.Context, trace TraceContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceContextKey{}, trace.Ensure())
}

// TraceFromContext 返回当前调用链中的 Trace。
func TraceFromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	trace, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return trace, ok && strings.TrimSpace(trace.TraceID) != ""
}

// RouteHash 只持久化路由键的不可逆摘要，避免飞书 session key 或用户 ID 明文落盘。
func RouteHash(routeKey string) string {
	routeKey = strings.TrimSpace(routeKey)
	if routeKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(routeKey))
	return hex.EncodeToString(sum[:traceRouteHashBytes])
}

// Event 是 Trace 存储的固定字段契约，不接受任意 map，避免调用方把凭据塞入 payload。
type Event struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`

	TraceID      string `json:"trace_id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	Stage        string `json:"stage"`
	State        string `json:"state,omitempty"`
	Source       string `json:"source,omitempty"`

	Platform  string `json:"platform,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	ChatID    string `json:"chat_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	RouteHash string `json:"route_hash,omitempty"`

	AgentName      string `json:"agent_name,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	CardID         string `json:"card_id,omitempty"`
	ReplyID        string `json:"reply_id,omitempty"`
	EventID        string `json:"event_id,omitempty"`
	Sequence       uint64 `json:"sequence,omitempty"`
	Kind           string `json:"kind,omitempty"`

	Direction string `json:"direction,omitempty"`
	Method    string `json:"method,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	WireEpoch uint64 `json:"wire_epoch,omitempty"`
	Payload   string `json:"payload,omitempty"`

	Summary string `json:"summary,omitempty"`
}

// EventFor 以当前 Trace 构造一条固定字段事件。
func EventFor(trace TraceContext, stage string, state string) Event {
	trace = trace.Ensure()
	return Event{
		TraceID: trace.TraceID, SpanID: trace.SpanID, ParentSpanID: trace.ParentSpanID,
		Stage: strings.TrimSpace(stage), State: strings.TrimSpace(state),
		Platform: trace.Platform, AccountID: trace.AccountID, ChatID: trace.ChatID,
		MessageID: trace.MessageID, ClientID: trace.ClientID, RouteHash: RouteHash(trace.RouteKey),
		AgentName: trace.AgentName, TaskID: trace.TaskID, ConversationID: trace.ConversationID,
		SessionID: trace.SessionID, ThreadID: trace.ThreadID, TurnID: trace.TurnID,
		CardID: trace.CardID, ReplyID: trace.ReplyID,
	}
}

var (
	bearerPattern           = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]+`)
	secretAssignmentPattern = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token|api[_-]?key|authorization|cookie|password|secret|token)\b\s*[:=]\s*[^\s,;]+`)
	jwtPattern              = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}(?:\.[a-zA-Z0-9_-]{4,})?\b`)
)

// SanitizeText 清理常见凭据并限制单条诊断摘要长度。
func SanitizeText(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	text = bearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = secretAssignmentPattern.ReplaceAllString(text, "$1=[REDACTED]")
	text = jwtPattern.ReplaceAllString(text, "[REDACTED_JWT]")
	runes := []rune(text)
	if len(runes) > traceSummaryMaxRunes {
		return string(runes[:traceSummaryMaxRunes]) + "…"
	}
	return text
}
