package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type toolRequestUserInputParams struct {
	ThreadID  string                              `json:"threadId"`
	TurnID    string                              `json:"turnId"`
	ItemID    string                              `json:"itemId"`
	Questions []codexDesktopUserInputWireQuestion `json:"questions"`
}

type toolRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type toolRequestUserInputResult struct {
	Answers map[string]toolRequestUserInputAnswer `json:"answers"`
}

func (a *ACPAgent) handleToolUserInputRequest(msg rpcResponse) error {
	if msg.ID == nil {
		return fmt.Errorf("user input server request has no id")
	}
	var params toolRequestUserInputParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return a.writeRPCServerError(*msg.ID, rpcInvalidParams, "Invalid requestUserInput params")
	}
	threadID := strings.TrimSpace(params.ThreadID)
	turnID := strings.TrimSpace(params.TurnID)
	itemID := strings.TrimSpace(params.ItemID)
	request := UserInputRequest{RequestID: strconv.FormatInt(*msg.ID, 10)}
	for _, wire := range params.Questions {
		request.Questions = append(request.Questions, UserInputQuestion{
			ID: strings.TrimSpace(wire.ID), Header: strings.TrimSpace(wire.Header),
			Prompt: firstNonEmpty(wire.Question, wire.Prompt), Options: wire.Options,
		})
	}
	if threadID == "" || turnID == "" || itemID == "" {
		return a.writeRPCServerError(*msg.ID, rpcInvalidParams, "Invalid requestUserInput route")
	}
	if err := validateCodexDesktopQuestions(request); err != nil {
		return a.writeRPCServerError(*msg.ID, rpcInvalidParams, "Invalid requestUserInput questions")
	}

	event := &codexTurnEvent{
		Kind: "user_input_request", TurnID: turnID, ItemID: itemID, Sequence: msg.Sequence,
		UserInput: &codexUserInputEvent{Request: request},
	}
	event.UserInput.Respond = func(_ context.Context, answers UserInputAnswers) error {
		if err := validateUserInputAnswers(request, answers); err != nil {
			return err
		}
		result := toolRequestUserInputResult{Answers: make(map[string]toolRequestUserInputAnswer, len(answers))}
		for questionID, values := range answers {
			result.Answers[questionID] = toolRequestUserInputAnswer{Answers: append([]string(nil), values...)}
		}
		err := a.writeRPCServerResponse(rpcServerResponse{
			JSONRPC: "2.0", ID: *msg.ID, Result: result,
		})
		if err == nil {
			a.forgetPendingCodexInteraction(threadID, event)
		}
		return err
	}
	a.dispatchToTurnCh(threadID, event)
	return nil
}
