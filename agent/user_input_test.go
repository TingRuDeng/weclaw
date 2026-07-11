package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestUserInputHandlerRoundTrip(t *testing.T) {
	want := UserInputAnswers{"question-1": {"answer"}}
	ctx := ContextWithUserInputHandler(context.Background(), func(context.Context, UserInputRequest) (UserInputAnswers, error) {
		return want, nil
	})
	handler := userInputHandlerFromContext(ctx)
	got, err := handler(ctx, UserInputRequest{RequestID: "request-1"})
	if err != nil || got["question-1"][0] != "answer" {
		t.Fatalf("handler() = %#v, %v", got, err)
	}
}

func TestUserInputAnswersRequireEveryQuestion(t *testing.T) {
	request := UserInputRequest{
		RequestID: "request-1",
		Questions: []UserInputQuestion{{ID: "question-1"}, {ID: "question-2"}},
	}
	if err := validateUserInputAnswers(request, UserInputAnswers{"question-1": {"yes"}}); err == nil {
		t.Fatal("validateUserInputAnswers() error = nil")
	}
	if err := validateUserInputAnswers(request, UserInputAnswers{
		"question-1": {"yes"}, "question-2": {""},
	}); err == nil {
		t.Fatal("empty answer error = nil")
	}
}

func TestCodexDesktopUserInputSendsValidatedAnswers(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	event, err := actions.userInputEvent("thread-1", desktopUserInputFixture())
	if err != nil {
		t.Fatalf("userInputEvent() error = %v", err)
	}
	answers := UserInputAnswers{"question-1": {"A"}}
	if err := event.UserInput.Respond(context.Background(), answers); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	if caller.calls[0].method != "thread-follower-submit-user-input" {
		t.Fatalf("method = %s", caller.calls[0].method)
	}
	payload := caller.calls[0].params.(codexDesktopUserInputPayload)
	if payload.RequestID != "request-1" || payload.Response.Answers["question-1"][0] != "A" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestCodexDesktopUserInputRejectsIncompleteAnswers(t *testing.T) {
	caller := &codexDesktopActionCaller{result: json.RawMessage(`{}`)}
	actions := newCodexDesktopActions(caller, func() string { return "sender" })
	event, err := actions.userInputEvent("thread-1", desktopUserInputFixture())
	if err != nil {
		t.Fatalf("userInputEvent() error = %v", err)
	}
	if err := event.UserInput.Respond(context.Background(), UserInputAnswers{}); err == nil {
		t.Fatal("Respond() error = nil")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("calls = %#v", caller.calls)
	}
}

func TestCodexDesktopUserInputRejectsEmptyQuestions(t *testing.T) {
	actions := newCodexDesktopActions(&codexDesktopActionCaller{}, func() string { return "sender" })
	action := desktopUserInputFixture()
	action.Params["questions"] = []any{}
	if _, err := actions.userInputEvent("thread-1", action); err == nil {
		t.Fatal("userInputEvent() error = nil")
	}
}

func desktopUserInputFixture() codexDesktopPendingAction {
	return codexDesktopPendingAction{
		ID: "request-1", Method: "item/tool/requestUserInput",
		Params: map[string]any{"questions": []any{map[string]any{
			"id": "question-1", "header": "选择", "question": "请选择",
			"options": []any{map[string]any{"label": "A", "description": "选项 A"}},
		}}},
	}
}
