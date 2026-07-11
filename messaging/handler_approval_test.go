package messaging

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestPendingApprovalIgnoresCodexNavigationChoice(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resultCh := make(chan string, 1)
	go func() {
		optionID, err := h.approvalHandlerForUser("ou_user", "ou_user", reply)(ctx, agent.ApprovalRequest{
			ToolCall: json.RawMessage(`{"cmd":"rm file"}`),
			Options: []agent.ApprovalOption{
				{ID: "allow_once", Name: "允许", Kind: "allow"},
				{ID: "deny_once", Name: "拒绝", Kind: "deny"},
			},
		})
		if err != nil {
			resultCh <- "error:" + err.Error()
			return
		}
		resultCh <- optionID
	}()
	waitUntil(t, func() bool { return hasPendingApprovalForTest(h, "ou_user") })

	h.HandleMessage(ctx, platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-nav-during-approval",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "/cx cd 0"},
		},
	}, reply)

	select {
	case got := <-resultCh:
		t.Fatalf("navigation choice should not resolve approval, got %q", got)
	case <-time.After(taskQueueProbeDelay):
	}

	h.HandleMessage(ctx, platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-approval-allow",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "allow_once"},
		},
	}, reply)

	select {
	case got := <-resultCh:
		if got != "allow_once" {
			t.Fatalf("approval result=%q, want allow_once", got)
		}
	case <-ctx.Done():
		t.Fatal("approval handler did not return")
	}
}

func TestPendingApprovalUsesApprovalKeyForConcurrentCards(t *testing.T) {
	h := NewHandler(nil, nil)
	replyA := newApprovalKeyCaptureReplier()
	replyB := newApprovalKeyCaptureReplier()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resultA := make(chan string, 1)
	resultB := make(chan string, 1)
	options := []agent.ApprovalOption{
		{ID: "allow_once", Name: "允许", Kind: "allow"},
		{ID: "deny_once", Name: "拒绝", Kind: "deny"},
	}

	go func() {
		optionID, err := h.approvalHandlerForUser("ou_user", "ou_user", replyA)(ctx, agent.ApprovalRequest{
			ToolCall: json.RawMessage(`{"cmd":"first"}`),
			Options:  options,
		})
		if err != nil {
			resultA <- "error:" + err.Error()
			return
		}
		resultA <- optionID
	}()
	go func() {
		optionID, err := h.approvalHandlerForUser("ou_user", "ou_user", replyB)(ctx, agent.ApprovalRequest{
			ToolCall: json.RawMessage(`{"cmd":"second"}`),
			Options:  options,
		})
		if err != nil {
			resultB <- "error:" + err.Error()
			return
		}
		resultB <- optionID
	}()

	keyA := replyA.waitApprovalKey(t, ctx)
	keyB := replyB.waitApprovalKey(t, ctx)
	if keyA == keyB {
		t.Fatalf("approval keys must isolate cards, got both %q", keyA)
	}

	h.HandleMessage(ctx, platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "approval-card-a",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "accept", "approval_key": keyA},
		},
	}, replyA)

	select {
	case got := <-resultA:
		if got != "allow_once" {
			t.Fatalf("approval A result=%q, want allow_once", got)
		}
	case <-ctx.Done():
		t.Fatal("approval A did not return")
	}
	select {
	case got := <-resultB:
		t.Fatalf("approval B should still be pending, got %q", got)
	case <-time.After(taskQueueProbeDelay):
	}
	if texts := replyA.textsSnapshot(); len(texts) != 0 {
		t.Fatalf("approval action was treated as normal message: %#v", texts)
	}

	h.HandleMessage(ctx, platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "approval-card-b",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "cancel", "approval_key": keyB},
		},
	}, replyB)

	select {
	case got := <-resultB:
		if got != "deny_once" {
			t.Fatalf("approval B result=%q, want deny_once", got)
		}
	case <-ctx.Done():
		t.Fatal("approval B did not return")
	}
	if texts := replyB.textsSnapshot(); len(texts) != 0 {
		t.Fatalf("approval action was treated as normal message: %#v", texts)
	}
}

func TestPendingApprovalIsolatesIdenticalConcurrentRequests(t *testing.T) {
	h := NewHandler(nil, nil)
	replyA := newApprovalKeyCaptureReplier()
	replyB := newApprovalKeyCaptureReplier()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	options := []agent.ApprovalOption{
		{ID: "allow_once", Name: "允许", Kind: "allow"},
		{ID: "deny_once", Name: "拒绝", Kind: "deny"},
	}
	request := agent.ApprovalRequest{
		RequestID: "101", ToolCall: json.RawMessage(`{"cmd":"same"}`), Options: options,
	}
	resultA := startApprovalForTest(ctx, h, replyA, request)
	request.RequestID = "102"
	resultB := startApprovalForTest(ctx, h, replyB, request)

	keyA := replyA.waitApprovalKey(t, ctx)
	keyB := replyB.waitApprovalKey(t, ctx)
	if keyA == keyB {
		t.Fatalf("identical approvals must use unique keys, got %q", keyA)
	}
	resolveApprovalForTest(t, ctx, h, replyA, keyA, "accept", resultA, "allow_once")
	assertApprovalPendingForTest(t, resultB)
	resolveApprovalForTest(t, ctx, h, replyB, keyB, "cancel", resultB, "deny_once")
}

func startApprovalForTest(ctx context.Context, h *Handler, reply platform.Replier, request agent.ApprovalRequest) <-chan string {
	result := make(chan string, 1)
	go func() {
		optionID, err := h.approvalHandlerForUser("ou_user", "ou_user", reply)(ctx, request)
		if err != nil {
			result <- "error:" + err.Error()
			return
		}
		result <- optionID
	}()
	return result
}

func resolveApprovalForTest(t *testing.T, ctx context.Context, h *Handler, reply platform.Replier, key string, choice string, result <-chan string, want string) {
	t.Helper()
	h.HandleMessage(ctx, platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": choice, "approval_key": key,
		}},
	}, reply)
	select {
	case got := <-result:
		if got != want {
			t.Fatalf("approval result=%q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatal("approval handler did not return")
	}
}

func assertApprovalPendingForTest(t *testing.T, result <-chan string) {
	t.Helper()
	select {
	case got := <-result:
		t.Fatalf("other approval should remain pending, got %q", got)
	case <-time.After(taskQueueProbeDelay):
	}
}

func TestExpiredApprovalActionDoesNotStartNewTask(t *testing.T) {
	ag := &fakeAgent{reply: "不应执行"}
	h := NewHandler(func(context.Context, string) agent.Agent { return ag }, nil)
	h.defaultName = "codex"
	reply := platformtest.NewReplier(platform.Capabilities{})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "expired-approval-card",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value: map[string]string{
				"choice":       "accept",
				"approval_key": "approval-expired",
			},
		},
	}, reply)

	if ag.wasChatCalled() {
		t.Fatalf("expired approval action must not start agent task, got message %q", ag.lastChatMessage())
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "授权请求已过期") {
		t.Fatalf("reply=%#v, want stale approval explanation", reply.Texts)
	}
}

func TestExpiredApprovalActionReportsResultWhenCallbackWaits(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{})
	resultCh := make(chan platform.CardActionResult, 1)

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "expired-approval-callback",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value: map[string]string{
				"choice":       "accept",
				"approval_key": "approval-expired",
			},
			Result: resultCh,
		},
	}, reply)

	select {
	case got := <-resultCh:
		if got != platform.CardActionResultExpired {
			t.Fatalf("result=%q, want expired", got)
		}
	default:
		t.Fatal("expired approval result was not reported")
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("callback path should rely on card update, got texts %#v", reply.Texts)
	}
}

func TestApprovalHandlerIncludesTaskCardIDMetadata(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newTaskCardMetadataReplier("card-task-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resultCh := make(chan string, 1)

	go func() {
		optionID, err := h.approvalHandlerForUser("ou_user", "ou_user", reply)(ctx, agent.ApprovalRequest{
			ToolCall: json.RawMessage(`{"cmd":"date"}`),
			Options: []agent.ApprovalOption{
				{ID: "accept", Name: "accept", Kind: "allow"},
				{ID: "cancel", Name: "cancel", Kind: "deny"},
			},
		})
		if err != nil {
			resultCh <- "error:" + err.Error()
			return
		}
		resultCh <- optionID
	}()

	choice := reply.waitChoice(t, ctx)
	if choice.Metadata["approval_key"] == "" {
		t.Fatalf("choice metadata=%#v, want approval key", choice.Metadata)
	}
	if choice.Metadata["task_card_id"] != "card-task-1" {
		t.Fatalf("choice metadata=%#v, want task card id", choice.Metadata)
	}
	h.HandleMessage(ctx, platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value: map[string]string{
				"choice":       "accept",
				"approval_key": choice.Metadata["approval_key"],
				"task_card_id": choice.Metadata["task_card_id"],
			},
		},
	}, reply)

	select {
	case got := <-resultCh:
		if got != "accept" {
			t.Fatalf("approval result=%q, want accept", got)
		}
	case <-ctx.Done():
		t.Fatal("approval handler did not return")
	}
}
