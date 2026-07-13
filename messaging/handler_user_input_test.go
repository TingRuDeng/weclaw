package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestUserInputHandlerCollectsEachQuestionAnswer(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newUserInputCaptureReplier()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan agent.UserInputAnswers, 1)
	go func() {
		answers, _ := h.userInputHandlerForRoute(interactionTestOptions(reply))(ctx, userInputTestRequest())
		result <- answers
	}()

	first := reply.waitRequest(t, ctx)
	assertUserInputRequest(t, first, "request-1:question-1", "快速")
	h.consumePendingApprovalForKey("user-1", first.key, "快速")
	second := reply.waitRequest(t, ctx)
	assertUserInputRequest(t, second, "request-1:question-2", "蓝色")
	h.consumePendingApprovalForKey("user-1", second.key, "蓝色")

	select {
	case answers := <-result:
		if answers["question-1"][0] != "快速" || answers["question-2"][0] != "蓝色" {
			t.Fatalf("answers = %#v", answers)
		}
	case <-ctx.Done():
		t.Fatal("结构化问答未返回")
	}
}

// TestUserInputRawCommandUsesCorrelationKey 验证并发问题只消费所属卡片的回答。
func TestUserInputRawCommandUsesCorrelationKey(t *testing.T) {
	h := NewHandler(nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	replyA, replyB := newUserInputCaptureReplier(), newUserInputCaptureReplier()
	resultA := startUserInputForTest(ctx, h, replyA, "request-a")
	resultB := startUserInputForTest(ctx, h, replyB, "request-b")
	requestA := replyA.waitRequest(t, ctx)
	requestB := replyB.waitRequest(t, ctx)

	h.HandleMessage(ctx, userInputCardMessage("answer-a", requestA.key, "快速"), replyA)
	assertUserInputResult(t, ctx, resultA, "快速")
	select {
	case got := <-resultB:
		t.Fatalf("问题 B 不应被问题 A 的卡片消费：%#v", got)
	case <-time.After(taskQueueProbeDelay):
	}
	h.HandleMessage(ctx, userInputCardMessage("answer-b", requestB.key, "完整"), replyB)
	assertUserInputResult(t, ctx, resultB, "完整")
}

// TestExpiredUserInputCardDoesNotBecomeAgentMessage 验证过期问题不会退化为普通消息。
func TestExpiredUserInputCardDoesNotBecomeAgentMessage(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newUserInputCaptureReplier()
	h.HandleMessage(context.Background(), userInputCardMessage("expired-question", "missing-key", "快速"), reply)
	if !containsText(reply.textsSnapshot(), "交互已过期") {
		t.Fatalf("texts=%#v，期望明确提示过期交互", reply.textsSnapshot())
	}
}

// startUserInputForTest 启动一个只有单题的结构化问答请求。
func startUserInputForTest(ctx context.Context, h *Handler, reply platform.Replier, requestID string) <-chan agent.UserInputAnswers {
	result := make(chan agent.UserInputAnswers, 1)
	go func() {
		request := agent.UserInputRequest{RequestID: requestID, Questions: []agent.UserInputQuestion{{
			ID: "question", Prompt: "请选择", Options: []agent.UserInputOption{{Label: "快速"}, {Label: "完整"}},
		}}}
		answers, _ := h.userInputHandlerForRoute(interactionTestOptions(reply))(ctx, request)
		result <- answers
	}()
	return result
}

// userInputCardMessage 构造携带问题关联键的飞书卡片回调消息。
func userInputCardMessage(messageID string, key string, choice string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "user-1", MessageID: messageID,
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": choice, "approval_key": key,
		}},
	}
}

// assertUserInputResult 验证结构化问答返回指定选项。
func assertUserInputResult(t *testing.T, ctx context.Context, result <-chan agent.UserInputAnswers, want string) {
	t.Helper()
	select {
	case answers := <-result:
		if len(answers["question"]) != 1 || answers["question"][0] != want {
			t.Fatalf("answers=%#v，期望 %q", answers, want)
		}
	case <-ctx.Done():
		t.Fatal("结构化问答未返回")
	}
}

func TestUserInputHandlerRejectsQuestionWithoutOptions(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newUserInputCaptureReplier()
	request := agent.UserInputRequest{RequestID: "request-1", Questions: []agent.UserInputQuestion{{ID: "question-1"}}}
	_, err := h.userInputHandlerForRoute(interactionTestOptions(reply))(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "不支持自由文本问答") {
		t.Fatalf("error = %v", err)
	}
}

func TestUserInputHandlerRejectsUnauthorizedRoute(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newUserInputCaptureReplier()
	opts := interactionTestOptions(reply)
	opts.routeUserID = "user-2"
	_, err := h.userInputHandlerForRoute(opts)(context.Background(), userInputTestRequest())
	if err == nil || !strings.Contains(err.Error(), "无权") {
		t.Fatalf("error = %v", err)
	}
}

func TestUserInputHandlerCancelsOutstandingChoicesOnContextDone(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newUserInputCaptureReplier()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := h.userInputHandlerForRoute(interactionTestOptions(reply))(ctx, userInputTestRequest())
		result <- err
	}()
	request := reply.waitRequest(t, ctx)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if h.consumePendingApprovalForKey("user-1", request.key, "快速") {
		t.Fatal("已取消的问题仍可被回答")
	}
}

type capturedUserInputRequest struct {
	prompt  string
	choices []platform.Choice
	key     string
}

type userInputCaptureReplier struct {
	*approvalKeyCaptureReplier
	requests chan capturedUserInputRequest
}

func newUserInputCaptureReplier() *userInputCaptureReplier {
	return &userInputCaptureReplier{
		approvalKeyCaptureReplier: newApprovalKeyCaptureReplier(),
		requests:                  make(chan capturedUserInputRequest, 2),
	}
}

func (r *userInputCaptureReplier) AskChoices(_ context.Context, prompt string, choices []platform.Choice) error {
	copyChoices := append([]platform.Choice(nil), choices...)
	r.requests <- capturedUserInputRequest{prompt: prompt, choices: copyChoices, key: approvalKeyFromChoices(choices)}
	return nil
}

func (r *userInputCaptureReplier) waitRequest(t *testing.T, ctx context.Context) capturedUserInputRequest {
	t.Helper()
	select {
	case request := <-r.requests:
		return request
	case <-ctx.Done():
		t.Fatal("未收到结构化问题卡片")
		return capturedUserInputRequest{}
	}
}

func interactionTestOptions(reply platform.Replier) agentInteractionContextOptions {
	return agentInteractionContextOptions{actorUserID: "user-1", routeUserID: "user-1", reply: reply}
}

func userInputTestRequest() agent.UserInputRequest {
	return agent.UserInputRequest{RequestID: "request-1", Questions: []agent.UserInputQuestion{
		{ID: "question-1", Header: "执行方式", Prompt: "请选择执行方式", Options: []agent.UserInputOption{{Label: "快速", Description: "优先速度"}, {Label: "完整"}}},
		{ID: "question-2", Header: "颜色", Prompt: "请选择颜色", Options: []agent.UserInputOption{{Label: "红色"}, {Label: "蓝色"}}},
	}}
}

func assertUserInputRequest(t *testing.T, request capturedUserInputRequest, key string, label string) {
	t.Helper()
	found := false
	for _, choice := range request.choices {
		found = found || strings.HasPrefix(choice.Label, label)
	}
	if request.key != key || len(request.choices) != 2 || !found {
		t.Fatalf("request = %#v", request)
	}
}
