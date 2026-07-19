package feishu

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// messageSender 抽象飞书消息发送能力，便于测试 Replier 行为。
type messageSender interface {
	SendText(ctx context.Context, openID string, text string) error
	SendImage(ctx context.Context, openID string, localPath string) error
	SendFile(ctx context.Context, openID string, localPath string) error
	SendCard(ctx context.Context, openID string, cardID string) error
	PatchCard(ctx context.Context, messageID string, cardJSON string) error
	ReplyText(ctx context.Context, messageID string, text string) error
	ReplyImage(ctx context.Context, messageID string, localPath string) error
	ReplyFile(ctx context.Context, messageID string, localPath string) error
	ReplyCard(ctx context.Context, messageID string, cardID string) error
}

type idempotentMessageSender interface {
	SendTextIdempotent(ctx context.Context, openID string, text string, operationID string) error
	ReplyTextIdempotent(ctx context.Context, messageID string, text string, operationID string) error
}

type createMessageFunc func(ctx context.Context, receiveID string, receiveIDType string, msgType string, content string) (int, string, error)
type replyMessageFunc func(ctx context.Context, messageID string, msgType string, content string, replyInThread bool) (int, string, error)
type patchMessageFunc func(ctx context.Context, messageID string, content string) (int, string, error)

type sdkMessageSender struct {
	client *lark.Client
	appID  string
	guide  *permissionGuideLimiter
	create createMessageFunc
	reply  replyMessageFunc
	patch  patchMessageFunc
}

// newSDKMessageSender 创建基于飞书 REST client 的消息发送器。
func newSDKMessageSender(client *lark.Client, appID string) messageSender {
	return &sdkMessageSender{client: client, appID: appID, guide: newPermissionGuideLimiter(appID)}
}

// SendText 通过 im.message.create 发送飞书文本消息。
func (s *sdkMessageSender) SendText(ctx context.Context, receiveID string, text string) error {
	content, err := buildTextMessageContent(text)
	if err != nil {
		return err
	}
	return s.createMessage(ctx, receiveID, larkim.MsgTypeText, content)
}

// SendTextIdempotent 使用飞书消息 UUID 去重同一终态文本重试。
func (s *sdkMessageSender) SendTextIdempotent(ctx context.Context, receiveID string, text string, operationID string) error {
	content, err := buildTextMessageContent(text)
	if err != nil {
		return err
	}
	return s.createMessageWithUUID(ctx, receiveID, larkim.MsgTypeText, content, operationID)
}

// SendImage 上传本地图片并发送 image 消息。
func (s *sdkMessageSender) SendImage(ctx context.Context, openID string, localPath string) error {
	content, err := s.imageMessageContent(ctx, openID, localPath)
	if err != nil {
		return err
	}
	return s.createMessage(ctx, openID, larkim.MsgTypeImage, content)
}

func (s *sdkMessageSender) imageMessageContent(ctx context.Context, guideTarget string, localPath string) (string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	imageResp, err := s.createImage(ctx, file)
	if err != nil {
		return "", err
	}
	if !imageResp.Success() || imageResp.Data == nil || imageResp.Data.ImageKey == nil {
		if guideTarget != "" {
			s.sendPermissionGuide(ctx, guideTarget, imageResp.Code)
		}
		return "", s.apiError(imageResp.Code, imageResp.Msg)
	}
	content, err := json.Marshal(map[string]string{"image_key": *imageResp.Data.ImageKey})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *sdkMessageSender) createImage(ctx context.Context, file *os.File) (*larkim.CreateImageResp, error) {
	imageReq := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType(larkim.CreateImageImageTypeMessage).
			Image(file).
			Build()).
		Build()
	return s.client.Im.Image.Create(ctx, imageReq)
}

// SendFile 上传本地文件并发送 file 消息。
func (s *sdkMessageSender) SendFile(ctx context.Context, openID string, localPath string) error {
	content, err := s.fileMessageContent(ctx, openID, localPath)
	if err != nil {
		return err
	}
	return s.createMessage(ctx, openID, larkim.MsgTypeFile, content)
}

func (s *sdkMessageSender) fileMessageContent(ctx context.Context, guideTarget string, localPath string) (string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	fileReq := larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType("stream").
			FileName(filepath.Base(localPath)).
			File(file).
			Build()).
		Build()
	fileResp, err := s.client.Im.File.Create(ctx, fileReq)
	if err != nil {
		return "", err
	}
	if !fileResp.Success() || fileResp.Data == nil || fileResp.Data.FileKey == nil {
		if guideTarget != "" {
			s.sendPermissionGuide(ctx, guideTarget, fileResp.Code)
		}
		return "", s.apiError(fileResp.Code, fileResp.Msg)
	}
	content, err := json.Marshal(map[string]string{"file_key": *fileResp.Data.FileKey})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// SendCard 发送已创建的 CardKit 卡片实例。
func (s *sdkMessageSender) SendCard(ctx context.Context, openID string, cardID string) error {
	content, err := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]string{"card_id": cardID},
	})
	if err != nil {
		return err
	}
	return s.createMessage(ctx, openID, "interactive", string(content))
}

// PatchCard 更新应用已经发送的交互卡片，供耗时按钮操作回写原卡。
func (s *sdkMessageSender) PatchCard(ctx context.Context, messageID string, cardJSON string) error {
	code, msg, err := s.patchCardRaw(ctx, messageID, cardJSON)
	if err != nil {
		return err
	}
	if code != 0 {
		return s.apiError(code, msg)
	}
	return nil
}

func (s *sdkMessageSender) patchCardRaw(ctx context.Context, messageID string, cardJSON string) (int, string, error) {
	if s.patch != nil {
		return s.patch(ctx, messageID, cardJSON)
	}
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(cardJSON).Build()).
		Build()
	resp, err := s.client.Im.V1.Message.Patch(ctx, req)
	if err != nil {
		return 0, "", err
	}
	if !resp.Success() {
		return resp.Code, resp.Msg, nil
	}
	return 0, "", nil
}

// createMessage 调用飞书消息创建接口，统一处理 API 错误。
func (s *sdkMessageSender) createMessage(ctx context.Context, receiveID string, msgType string, content string) error {
	return s.createMessageWithUUID(ctx, receiveID, msgType, content, "")
}

func (s *sdkMessageSender) createMessageWithUUID(ctx context.Context, receiveID string, msgType string, content string, operationID string) error {
	code, msg, err := s.createMessageRawWithUUID(ctx, receiveID, msgType, content, operationID)
	if err != nil {
		return err
	}
	if code != 0 {
		s.sendPermissionGuide(ctx, receiveID, code)
		return s.apiError(code, msg)
	}
	return nil
}

func (s *sdkMessageSender) createMessageRaw(ctx context.Context, receiveID string, msgType string, content string) (int, string, error) {
	return s.createMessageRawWithUUID(ctx, receiveID, msgType, content, "")
}

func (s *sdkMessageSender) createMessageRawWithUUID(ctx context.Context, receiveID string, msgType string, content string, operationID string) (int, string, error) {
	receiveIDType := feishuReceiveIDType(receiveID)
	if s.create != nil {
		return s.create(ctx, receiveID, receiveIDType, msgType, content)
	}
	body := larkim.NewCreateMessageReqBodyBuilder().
		ReceiveId(receiveID).
		MsgType(msgType).
		Content(content)
	if strings.TrimSpace(operationID) != "" {
		body.Uuid(operationID)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(body.Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return 0, "", err
	}
	if !resp.Success() {
		return resp.Code, resp.Msg, nil
	}
	return 0, "", nil
}

// feishuReceiveIDType 根据 ID 前缀选择飞书消息接收 ID 类型。
func feishuReceiveIDType(receiveID string) string {
	if strings.HasPrefix(receiveID, "oc_") {
		return larkim.CreateMessageV1ReceiveIDTypeChatId
	}
	if strings.HasPrefix(receiveID, "on_") {
		return larkim.CreateMessageV1ReceiveIDTypeUnionId
	}
	return larkim.CreateMessageV1ReceiveIDTypeOpenId
}

func (s *sdkMessageSender) sendPermissionGuide(ctx context.Context, openID string, code int) {
	guide, ok := s.guide.MessageForCode(code)
	if !ok {
		return
	}
	log.Printf("[feishu] %s", guide)
	if s.sendPermissionGuideCard(ctx, openID) {
		return
	}
	s.sendPermissionGuideText(ctx, openID, guide)
}

func (s *sdkMessageSender) sendPermissionGuideCard(ctx context.Context, openID string) bool {
	card, err := buildPermissionGuideCard(s.appID, true)
	if err != nil {
		log.Printf("[feishu] 权限引导卡片构建失败: %v", err)
		return false
	}
	if err := s.sendMessageWithoutGuide(ctx, openID, "interactive", card); err != nil {
		log.Printf("[feishu] 权限引导卡片发送失败: %v", err)
		return false
	}
	return true
}

func (s *sdkMessageSender) sendPermissionGuideText(ctx context.Context, openID string, guide string) {
	text, err := buildTextMessageContent(guide)
	if err != nil {
		log.Printf("[feishu] 权限引导文本构建失败: %v", err)
		return
	}
	if err := s.sendMessageWithoutGuide(ctx, openID, larkim.MsgTypeText, text); err != nil {
		log.Printf("[feishu] 权限引导文本发送失败: %v", err)
	}
}

func (s *sdkMessageSender) sendMessageWithoutGuide(ctx context.Context, openID string, msgType string, content string) error {
	code, msg, err := s.createMessageRaw(ctx, openID, msgType, content)
	if err != nil {
		return err
	}
	if code != 0 {
		return formatFeishuAPIError(s.appID, code, msg)
	}
	return nil
}

// apiError 统一生成不包含 app_secret 的飞书 API 错误。
func (s *sdkMessageSender) apiError(code int, msg string) error {
	return formatFeishuAPIError(s.appID, code, msg)
}

func buildTextMessageContent(text string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	return string(content), nil
}
