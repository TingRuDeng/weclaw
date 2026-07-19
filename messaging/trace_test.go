package messaging

import (
	"context"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type traceCapture struct {
	mu     sync.Mutex
	events []observability.Event
}

func (capture *traceCapture) Record(event observability.Event) error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.events = append(capture.events, event)
	return nil
}

func (capture *traceCapture) snapshot() []observability.Event {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]observability.Event(nil), capture.events...)
}

func TestHandleMessageRecordsOneTraceAcrossTaskProgressAndTerminal(t *testing.T) {
	ag := &fakeStructuredProgressAgent{
		fakeProgressAgent: fakeProgressAgent{fakeAgent: fakeAgent{
			reply: "完成", info: agent.AgentInfo{Name: "mock", Type: "test"},
		}},
		events: []agent.ProgressEvent{{
			ID: "tool-1", Kind: agent.ProgressKindTool, State: agent.ProgressStateRunning,
			Sequence: 7, Summary: "执行测试",
		}},
	}
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("mock", ag)
	capture := &traceCapture{}
	h.SetTraceRecorder(capture)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "app-1", UserID: "user-1",
		ChatID: "chat-1", MessageID: "message-1", Text: "运行任务",
	}, reply)

	events := capture.snapshot()
	wantStages := []string{"message.received", "message.accepted", "agent.dispatched", "task.started", "task.progress", "task.completed"}
	stages := make(map[string]observability.Event, len(events))
	for _, event := range events {
		stages[event.Stage] = event
	}
	rootTrace := stages["message.received"].TraceID
	if rootTrace == "" {
		t.Fatalf("events=%#v", events)
	}
	for _, stage := range wantStages {
		event, ok := stages[stage]
		if !ok {
			t.Fatalf("missing stage %q in %#v", stage, events)
		}
		if event.TraceID != rootTrace {
			t.Fatalf("stage %s trace=%q, want %q", stage, event.TraceID, rootTrace)
		}
	}
	progress := stages["task.progress"]
	if progress.EventID != "tool-1" || progress.Sequence != 7 || progress.Kind != string(agent.ProgressKindTool) || progress.TaskID == "" {
		t.Fatalf("progress=%#v", progress)
	}
}

func TestHandleMessageRecordsDuplicateWithoutAcceptingIt(t *testing.T) {
	h := NewHandler(nil, nil)
	capture := &traceCapture{}
	h.SetTraceRecorder(capture)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	message := platform.IncomingMessage{
		Platform: platform.PlatformWeChat, AccountID: "bot-1", UserID: "user-1",
		ChatID: "user-1", MessageID: "message-1", Text: "hello",
	}
	h.HandleMessage(context.Background(), message, reply)
	h.HandleMessage(context.Background(), message, reply)

	events := capture.snapshot()
	duplicates := 0
	accepted := 0
	for _, event := range events {
		switch event.Stage {
		case "message.duplicate":
			duplicates++
		case "message.accepted":
			accepted++
		}
	}
	if duplicates != 1 || accepted != 1 {
		t.Fatalf("duplicates=%d accepted=%d events=%#v", duplicates, accepted, events)
	}
}

func TestBroadcastEarlyFailureKeepsAgentBranchTraceThroughReplyDelivery(t *testing.T) {
	h := NewHandler(nil, nil)
	capture := &traceCapture{}
	h.SetTraceRecorder(capture)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	root := observability.NewTraceContext(observability.TraceSeed{
		Platform: string(platform.PlatformFeishu), MessageID: "message-1",
	})
	h.broadcastToAgents(broadcastAgentsRequest{
		ctx:          observability.ContextWithTrace(context.Background(), root),
		platformName: platform.PlatformFeishu, userID: "user-1", routeUserID: "route-1",
		replyWriter: reply, names: []string{"missing"}, message: "run", trace: root,
	})

	events := capture.snapshot()
	var dispatched observability.Event
	var delivered observability.Event
	for _, event := range events {
		switch event.Stage {
		case "agent.dispatched":
			dispatched = event
		case "reply.delivery.completed":
			delivered = event
		}
	}
	if dispatched.TraceID != root.TraceID || delivered.TraceID != root.TraceID || delivered.SpanID != dispatched.SpanID {
		t.Fatalf("events=%#v", events)
	}
	if dispatched.ParentSpanID != root.SpanID || delivered.AgentName != "missing" {
		t.Fatalf("dispatched=%#v delivered=%#v", dispatched, delivered)
	}
}
