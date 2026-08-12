package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestSendEmptyReplyProjectionSkipsFollowerGuardTrace(t *testing.T) {
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	h := NewHandler(nil, nil)
	capture := &traceCapture{}
	h.SetTraceRecorder(capture)
	h.sendReplyProjection(replyDeliveryRequest{
		ctx: context.Background(), replyWriter: newOutboxTestReplier(route),
		trace: observability.NewTraceContext(observability.TraceSeed{Platform: string(platform.PlatformFeishu)}),
		deliveryGuard: terminalDeliveryGuard{
			AuthorizedIdentity: "test-user", FollowerBindingKey: "missing-binding",
			FollowerRevision: 1, FollowerThreadID: "thread-1",
		},
	}, replyDeliveryProjection{}, true)

	if events := capture.snapshot(); len(events) != 0 {
		t.Fatalf("empty projection trace events=%#v, want no delivery attempt or suppression", events)
	}
}

func TestSendReplyProjectionReportsFollowerRevisionChange(t *testing.T) {
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_chat"}
	reply := newOutboxTestReplier(route)
	h := NewHandler(nil, nil)
	h.SetPlatformRegistry(newOutboxTestRegistry(route, reply))
	capture := &traceCapture{}
	h.SetTraceRecorder(capture)
	bindingKey := codexBindingKey("route-user", "codex")
	workspace := "/workspace/jumpserver"
	store := h.ensureCodexSessions()
	store.mu.Lock()
	store.bindings[bindingKey] = codexSessionBinding{
		ActiveWorkspace: workspace,
		Workspaces:      map[string]codexWorkspaceSession{workspace: {ThreadID: "thread-1"}},
		FollowRevision:  2,
		Follower: &codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-1", ActorUserID: "user-1",
			AuthorizedIdentity: "test-user", DeliveryRoute: route,
		},
	}
	store.mu.Unlock()
	trace := observability.NewTraceContext(observability.TraceSeed{Platform: string(platform.PlatformFeishu)})
	h.sendReplyProjection(replyDeliveryRequest{
		ctx: context.Background(), replyWriter: reply, trace: trace,
		deliveryGuard: terminalDeliveryGuard{
			AuthorizedIdentity: "test-user", FollowerBindingKey: bindingKey,
			FollowerRevision: 1, FollowerThreadID: "thread-1",
		},
	}, replyDeliveryProjection{text: "最终结果"}, false)

	events := capture.snapshot()
	if len(events) != 1 || events[0].Stage != "reply.delivery.suppressed" ||
		!strings.Contains(events[0].Summary, "revision_changed") {
		t.Fatalf("events=%#v, want precise revision_changed suppression", events)
	}
}
