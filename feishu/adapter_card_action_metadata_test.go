package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

// TestHandleCardActionEventPreservesQuestionCorrelation 验证普通问答卡片不会丢失关联字段。
func TestHandleCardActionEventPreservesQuestionCorrelation(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Context:  &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_question"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": cardActionChoice, "choice": "快速", "approval_key": "question-1",
			"kind":         platform.ChoiceInteractionUserInput,
			"task_card_id": "card-task-1", "feishu_session_key": "feishu:tenant:dm:oc_chat:ou_user",
		}},
	}}
	dispatched := make(chan platform.IncomingMessage, 1)

	_, err := adapter.handleCardActionEvent(context.Background(), event, func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		dispatched <- msg
	})
	if err != nil {
		t.Fatalf("处理结构化问答回调失败：%v", err)
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value["approval_key"] != "question-1" || msg.RawCommand.Value["task_card_id"] != "card-task-1" ||
			msg.RawCommand.Value[platform.ChoiceMetadataInteractionKind] != platform.ChoiceInteractionUserInput {
			t.Fatalf("RawCommand=%#v，期望保留问题关联字段", msg.RawCommand)
		}
	case <-time.After(time.Second):
		t.Fatal("等待结构化问答回调超时")
	}
}

// TestHandleCardActionEventPreservesModelAgent 验证模型卡片目标 Agent 能进入业务层。
func TestHandleCardActionEventPreservesModelAgent(t *testing.T) {
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_user"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			"action": cardActionChoice, "choice": "/reasoning high", modelSettingAgentKey: "claude",
		}},
	}}
	dispatched := make(chan platform.IncomingMessage, 1)

	_, err := adapter.handleCardActionEvent(context.Background(), event, func(_ context.Context, msg platform.IncomingMessage, _ platform.Replier) {
		dispatched <- msg
	})
	if err != nil {
		t.Fatalf("处理模型卡片回调失败：%v", err)
	}
	select {
	case msg := <-dispatched:
		if msg.RawCommand.Value[modelSettingAgentKey] != "claude" {
			t.Fatalf("RawCommand=%#v，期望保留目标 Agent", msg.RawCommand)
		}
	case <-time.After(time.Second):
		t.Fatal("等待模型卡片回调超时")
	}
}
