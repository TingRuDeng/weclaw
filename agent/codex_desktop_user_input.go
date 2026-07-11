package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type codexDesktopUserInputPayload struct {
	ConversationID string `json:"conversationId"`
	RequestID      string `json:"requestId"`
	Response       struct {
		Answers UserInputAnswers `json:"answers"`
	} `json:"response"`
}

type codexDesktopUserInputWireQuestion struct {
	ID       string            `json:"id"`
	Header   string            `json:"header"`
	Question string            `json:"question"`
	Prompt   string            `json:"prompt"`
	Options  []UserInputOption `json:"options"`
}

// userInputEvent 把 Desktop pending question 投影为统一结构化问答事件。
func (a *codexDesktopActions) userInputEvent(threadID string, action codexDesktopPendingAction) (*codexTurnEvent, error) {
	if action.Method != "item/tool/requestUserInput" {
		return nil, fmt.Errorf("Desktop action %q 不是结构化问答", action.Method)
	}
	request, err := decodeCodexDesktopUserInput(action)
	if err != nil {
		return nil, err
	}
	event := &codexUserInputEvent{Request: request}
	event.Respond = func(ctx context.Context, answers UserInputAnswers) error {
		if err := validateUserInputAnswers(request, answers); err != nil {
			return err
		}
		payload := codexDesktopUserInputPayload{
			ConversationID: strings.TrimSpace(threadID), RequestID: action.ID,
		}
		payload.Response.Answers = answers
		_, err := a.client.Call(ctx, "thread-follower-submit-user-input", payload)
		return err
	}
	return &codexTurnEvent{Kind: "user_input_request", UserInput: event}, nil
}

// decodeCodexDesktopUserInput 校验问题列表并兼容 question/prompt 字段。
func decodeCodexDesktopUserInput(action codexDesktopPendingAction) (UserInputRequest, error) {
	encoded, err := json.Marshal(action.Params["questions"])
	if err != nil {
		return UserInputRequest{}, fmt.Errorf("编码 Desktop questions: %w", err)
	}
	var wireQuestions []codexDesktopUserInputWireQuestion
	if err := json.Unmarshal(encoded, &wireQuestions); err != nil {
		return UserInputRequest{}, fmt.Errorf("解析 Desktop questions: %w", err)
	}
	request := UserInputRequest{RequestID: action.ID}
	for _, wire := range wireQuestions {
		request.Questions = append(request.Questions, UserInputQuestion{
			ID: strings.TrimSpace(wire.ID), Header: strings.TrimSpace(wire.Header),
			Prompt: firstNonEmpty(wire.Question, wire.Prompt), Options: wire.Options,
		})
	}
	if err := validateCodexDesktopQuestions(request); err != nil {
		return UserInputRequest{}, err
	}
	return request, nil
}

// validateCodexDesktopQuestions 拒绝无法在消息平台展示或回答的问题。
func validateCodexDesktopQuestions(request UserInputRequest) error {
	if len(request.Questions) == 0 {
		return fmt.Errorf("Desktop 结构化问答 questions 为空")
	}
	for _, question := range request.Questions {
		if question.ID == "" || question.Prompt == "" {
			return fmt.Errorf("Desktop 结构化问答缺少 question id 或 prompt")
		}
	}
	return nil
}
