package feishu

import (
	"context"
	"encoding/json"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ReplyText 通过 im.message.reply 把文本回复到原消息 / 话题。
func (s *sdkMessageSender) ReplyText(ctx context.Context, messageID string, text string) error {
	content, err := buildTextMessageContent(text)
	if err != nil {
		return err
	}
	return s.replyMessage(ctx, messageID, larkim.MsgTypeText, content)
}

// ReplyImage 上传本地图片并回复到原消息 / 话题。
func (s *sdkMessageSender) ReplyImage(ctx context.Context, messageID string, localPath string) error {
	content, err := s.imageMessageContent(ctx, "", localPath)
	if err != nil {
		return err
	}
	return s.replyMessage(ctx, messageID, larkim.MsgTypeImage, content)
}

// ReplyFile 上传本地文件并回复到原消息 / 话题。
func (s *sdkMessageSender) ReplyFile(ctx context.Context, messageID string, localPath string) error {
	content, err := s.fileMessageContent(ctx, "", localPath)
	if err != nil {
		return err
	}
	return s.replyMessage(ctx, messageID, larkim.MsgTypeFile, content)
}

// ReplyCard 发送已创建的 CardKit 卡片实例到原消息 / 话题。
func (s *sdkMessageSender) ReplyCard(ctx context.Context, messageID string, cardID string) error {
	content, err := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]string{"card_id": cardID},
	})
	if err != nil {
		return err
	}
	return s.replyMessage(ctx, messageID, "interactive", string(content))
}

// replyMessage 调用飞书消息回复接口，统一处理 API 错误。
func (s *sdkMessageSender) replyMessage(ctx context.Context, messageID string, msgType string, content string) error {
	code, msg, err := s.replyMessageRaw(ctx, messageID, msgType, content)
	if err != nil {
		return err
	}
	if code != 0 {
		return s.apiError(code, msg)
	}
	return nil
}

func (s *sdkMessageSender) replyMessageRaw(ctx context.Context, messageID string, msgType string, content string) (int, string, error) {
	if s.reply != nil {
		return s.reply(ctx, messageID, msgType, content, true)
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := s.client.Im.V1.Message.Reply(ctx, req)
	if err != nil {
		return 0, "", err
	}
	if !resp.Success() {
		return resp.Code, resp.Msg, nil
	}
	return 0, "", nil
}
