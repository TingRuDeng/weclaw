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
