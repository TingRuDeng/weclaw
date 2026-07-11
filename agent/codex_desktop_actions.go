package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type codexDesktopActionClient interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}

type codexDesktopActions struct {
	client    codexDesktopActionClient
	requestID func() string
}

type codexDesktopStartTurnSpec struct {
	ConversationID, Cwd, ApprovalPolicy, ApprovalsReviewer string
	Input                                                  []codexUserInput
	SandboxPolicy                                          any
	Model, Effort                                          string
}

type codexDesktopStartTurnPayload struct {
	ConversationID  string               `json:"conversationId"`
	SenderRequestID string               `json:"senderRequestId"`
	TurnStartParams codexTurnStartParams `json:"turnStartParams"`
}

type codexDesktopSteerTurnSpec struct {
	ConversationID, ExpectedTurnID, Message string
}

type codexDesktopSteerTurnPayload struct {
	ConversationID string           `json:"conversationId"`
	Input          []codexUserInput `json:"input"`
	ExpectedTurnID string           `json:"expectedTurnId"`
}

type codexDesktopInterruptTurnPayload struct {
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
}

// newCodexDesktopActions 创建不自动重试变更请求的 Desktop 操作入口。
func newCodexDesktopActions(client codexDesktopActionClient, requestID func() string) *codexDesktopActions {
	return &codexDesktopActions{client: client, requestID: requestID}
}

// startTurn 在一次逻辑发送内固定 senderRequestId，并提取权威 turn ID。
func (a *codexDesktopActions) startTurn(ctx context.Context, spec codexDesktopStartTurnSpec) (string, error) {
	payload := codexDesktopStartTurnPayload{
		ConversationID: strings.TrimSpace(spec.ConversationID), SenderRequestID: a.requestID(),
		TurnStartParams: codexTurnStartParams{
			ThreadID: strings.TrimSpace(spec.ConversationID), Input: spec.Input, Cwd: spec.Cwd,
			ApprovalPolicy: spec.ApprovalPolicy, ApprovalsReviewer: spec.ApprovalsReviewer,
			SandboxPolicy: spec.SandboxPolicy, Model: spec.Model, Effort: spec.Effort,
		},
	}
	result, err := a.client.Call(ctx, "thread-follower-start-turn", payload)
	if err != nil {
		return "", err
	}
	turnID := codexDesktopTurnIDFromStartResult(result)
	if turnID == "" {
		return "", fmt.Errorf("Codex Desktop start turn 响应缺少 turn.id")
	}
	return turnID, nil
}

// steerTurn 把补充消息限定到调用方确认的 active turn。
func (a *codexDesktopActions) steerTurn(ctx context.Context, spec codexDesktopSteerTurnSpec) error {
	payload := codexDesktopSteerTurnPayload{
		ConversationID: strings.TrimSpace(spec.ConversationID),
		ExpectedTurnID: strings.TrimSpace(spec.ExpectedTurnID),
		Input:          []codexUserInput{{Type: "text", Text: spec.Message}},
	}
	_, err := a.client.Call(ctx, "thread-follower-steer-turn", payload)
	return err
}

// interruptTurn 使用 v2 follower 方法停止指定 turn。
func (a *codexDesktopActions) interruptTurn(ctx context.Context, conversationID string, turnID string) error {
	payload := codexDesktopInterruptTurnPayload{
		ConversationID: strings.TrimSpace(conversationID), TurnID: strings.TrimSpace(turnID),
	}
	_, err := a.client.Call(ctx, "thread-follower-interrupt-turn", payload)
	return err
}

// codexDesktopTurnIDFromStartResult 兼容包装与直接两种 Desktop 返回形态。
func codexDesktopTurnIDFromStartResult(result json.RawMessage) string {
	var response struct {
		Result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		} `json:"result"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(result, &response) != nil {
		return ""
	}
	return firstNonEmpty(response.Result.Turn.ID, response.Turn.ID)
}
