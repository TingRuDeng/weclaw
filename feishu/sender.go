package feishu

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// messageSender 抽象飞书消息发送能力，便于测试 Replier 行为。
type messageSender interface {
	SendText(ctx context.Context, openID string, text string) error
	SendImage(ctx context.Context, openID string, localPath string) error
	SendCard(ctx context.Context, openID string, cardID string) error
}

type createMessageFunc func(ctx context.Context, receiveID string, receiveIDType string, msgType string, content string) (int, string, error)

type sdkMessageSender struct {
	client *lark.Client
	appID  string
	guide  *permissionGuideLimiter
	create createMessageFunc
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

// SendImage 上传本地图片并发送 image 消息。
func (s *sdkMessageSender) SendImage(ctx context.Context, openID string, localPath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	imageResp, err := s.createImage(ctx, file)
	if err != nil {
		return err
	}
	if !imageResp.Success() || imageResp.Data == nil || imageResp.Data.ImageKey == nil {
		s.sendPermissionGuide(ctx, openID, imageResp.Code)
		return s.apiError(imageResp.Code, imageResp.Msg)
	}
	content, err := json.Marshal(map[string]string{"image_key": *imageResp.Data.ImageKey})
	if err != nil {
		return err
	}
	return s.createMessage(ctx, openID, larkim.MsgTypeImage, string(content))
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

// createMessage 调用飞书消息创建接口，统一处理 API 错误。
func (s *sdkMessageSender) createMessage(ctx context.Context, receiveID string, msgType string, content string) error {
	code, msg, err := s.createMessageRaw(ctx, receiveID, msgType, content)
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
	receiveIDType := feishuReceiveIDType(receiveID)
	if s.create != nil {
		return s.create(ctx, receiveID, receiveIDType, msgType, content)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType(msgType).
			Content(content).
			Build()).
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
