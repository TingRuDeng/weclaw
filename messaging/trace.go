package messaging

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

const traceErrorLogInterval = time.Minute

func newPlatformMessageTrace(msg platform.IncomingMessage, routeUserID string) observability.TraceContext {
	return observability.NewTraceContext(observability.TraceSeed{
		Platform: string(msg.Platform), AccountID: msg.AccountID, ChatID: msg.ChatID,
		MessageID: msg.MessageID, RouteKey: routeUserID,
	})
}

func (h *Handler) recordTrace(event observability.Event) {
	if h == nil || h.traceRecorder == nil {
		return
	}
	if err := h.traceRecorder.Record(event); err != nil {
		h.traceErrorMu.Lock()
		now := time.Now()
		shouldLog := h.lastTraceErrorAt.IsZero() || now.Sub(h.lastTraceErrorAt) >= traceErrorLogInterval
		if shouldLog {
			h.lastTraceErrorAt = now
		}
		h.traceErrorMu.Unlock()
		if shouldLog {
			log.Printf("[trace] record failed: %v", err)
		}
	}
}

func (h *Handler) recordTraceStage(trace observability.TraceContext, stage string, state string, summary string) {
	if strings.TrimSpace(trace.TraceID) == "" {
		return
	}
	event := observability.EventFor(trace, stage, state)
	event.Summary = summary
	h.recordTrace(event)
}

func (h *Handler) recordProgressTrace(trace observability.TraceContext, event agent.ProgressEvent, display string) {
	if strings.TrimSpace(trace.TraceID) == "" {
		return
	}
	record := observability.EventFor(trace, "task.progress", string(event.State))
	record.Source = "agent"
	record.EventID = strings.TrimSpace(event.ID)
	record.Sequence = event.Sequence
	record.Kind = string(event.Kind)
	record.Summary = display
	h.recordTrace(record)
}

func (h *Handler) recordTaskAdmissionTrace(trace observability.TraceContext, status activeTaskAdmissionStatus) {
	switch status {
	case activeTaskQueued:
		h.recordTraceStage(trace, "task.queued", "pending", "queued behind active task")
	case activeTaskForeignWriter:
		h.recordTraceStage(trace, "task.rejected", "busy", "session has another writer")
	case activeTaskPendingOccupied:
		h.recordTraceStage(trace, "task.rejected", "busy", "pending task slot occupied")
	case activeTaskMissing:
		h.recordTraceStage(trace, "task.rejected", "missing", "active task disappeared")
	}
}

func traceSummaryForIncoming(msg platform.IncomingMessage, text string) string {
	return fmt.Sprintf("text_runes=%d attachments=%d card_action=%t", len([]rune(text)), len(msg.Attachments), msg.RawCommand != nil)
}

func traceWithReply(trace observability.TraceContext, reply platform.Replier) observability.TraceContext {
	if strings.TrimSpace(trace.TraceID) == "" {
		return observability.TraceContext{}
	}
	cardID := ""
	if reporter, ok := reply.(platform.TaskCardReporter); ok {
		cardID = reporter.CurrentTaskCardID()
	}
	replyID := ""
	if reporter, ok := reply.(platform.DeliveryRouteReporter); ok {
		replyID = reporter.DeliveryRoute().ReplyToID
	}
	return trace.WithDelivery(cardID, replyID)
}

func (t *activeAgentTask) traceSnapshot() observability.TraceContext {
	if t == nil {
		return observability.TraceContext{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	trace := t.trace
	trace.AgentName = t.agentName
	trace.TaskID = t.taskID
	trace.ConversationID = firstNonBlank(trace.ConversationID, t.conversationID)
	trace.SessionID = firstNonBlank(trace.SessionID, t.sessionID)
	trace.ThreadID = firstNonBlank(t.codexThreadID, trace.ThreadID)
	trace.TurnID = firstNonBlank(t.codexTurnID, trace.TurnID)
	return trace.Ensure()
}

func (t *activeAgentTask) setTraceConversation(conversationID string, sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.conversationID = strings.TrimSpace(conversationID)
	if strings.TrimSpace(sessionID) != "" {
		t.sessionID = strings.TrimSpace(sessionID)
	}
	t.trace = t.trace.WithConversation(t.conversationID).WithSession(t.sessionID)
	t.mu.Unlock()
}

func (t *activeAgentTask) setTraceThreadTurn(threadID string, turnID string) observability.TraceContext {
	if t == nil {
		return observability.TraceContext{}
	}
	t.mu.Lock()
	if strings.TrimSpace(threadID) != "" {
		t.codexThreadID = strings.TrimSpace(threadID)
	}
	if strings.TrimSpace(turnID) != "" {
		t.codexTurnID = strings.TrimSpace(turnID)
	}
	t.trace = t.trace.WithThreadTurn(t.codexThreadID, t.codexTurnID)
	trace := t.trace
	t.mu.Unlock()
	return trace
}
