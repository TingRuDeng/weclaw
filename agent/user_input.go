package agent

import (
	"context"
	"fmt"
	"strings"
)

type userInputContextKey struct{}

type UserInputOption struct {
	Label       string
	Description string
}

type UserInputQuestion struct {
	ID      string
	Header  string
	Prompt  string
	Options []UserInputOption
}

type UserInputRequest struct {
	RequestID string
	Questions []UserInputQuestion
}

type UserInputAnswers map[string][]string

type UserInputHandler func(context.Context, UserInputRequest) (UserInputAnswers, error)

type codexUserInputEvent struct {
	Request UserInputRequest
	Respond func(context.Context, UserInputAnswers) error
	Retry   func()
}

// ContextWithUserInputHandler 为当前任务注入结构化问答处理器。
func ContextWithUserInputHandler(ctx context.Context, handler UserInputHandler) context.Context {
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, userInputContextKey{}, handler)
}

// userInputHandlerFromContext 读取当前任务的结构化问答处理器。
func userInputHandlerFromContext(ctx context.Context) UserInputHandler {
	handler, _ := ctx.Value(userInputContextKey{}).(UserInputHandler)
	return handler
}

// validateUserInputAnswers 要求每个 question ID 都有至少一个非空答案。
func validateUserInputAnswers(request UserInputRequest, answers UserInputAnswers) error {
	if len(request.Questions) == 0 {
		return fmt.Errorf("结构化问答不包含问题")
	}
	for _, question := range request.Questions {
		if strings.TrimSpace(question.ID) == "" {
			return fmt.Errorf("结构化问答包含空 question ID")
		}
		values, ok := answers[question.ID]
		if !ok || !hasNonEmptyUserInputAnswer(values) {
			return fmt.Errorf("问题 %s 缺少非空答案", question.ID)
		}
	}
	return nil
}

// hasNonEmptyUserInputAnswer 判断答案数组是否至少包含一项可见文本。
func hasNonEmptyUserInputAnswer(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// handleCodexUserInputEvent 调用消息层 handler，并把完整答案交给 provider responder。
func (a *ACPAgent) handleCodexUserInputEvent(ctx context.Context, evt *codexTurnEvent) error {
	if evt == nil || evt.UserInput == nil {
		return nil
	}
	handler := userInputHandlerFromContext(ctx)
	if handler == nil {
		return retryCodexUserInputEvent(evt, fmt.Errorf("Codex 结构化问答缺少消息处理器"))
	}
	answers, err := handler(ctx, evt.UserInput.Request)
	if err != nil {
		return retryCodexUserInputEvent(evt, err)
	}
	if err := validateUserInputAnswers(evt.UserInput.Request, answers); err != nil {
		return retryCodexUserInputEvent(evt, err)
	}
	if evt.UserInput.Respond == nil {
		return retryCodexUserInputEvent(evt, fmt.Errorf("Codex 结构化问答缺少 provider responder"))
	}
	return evt.UserInput.Respond(ctx, answers)
}

// retryCodexUserInputEvent 释放投递标记，让未回答请求可由后续 snapshot 重投。
func retryCodexUserInputEvent(evt *codexTurnEvent, err error) error {
	if evt != nil && evt.UserInput != nil && evt.UserInput.Retry != nil {
		evt.UserInput.Retry()
	}
	return err
}
