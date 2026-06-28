package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const feishuTextChunkRunes = 30000

// messageSender 抽象飞书消息发送能力，便于测试 Replier 行为。
type messageSender interface {
	SendText(ctx context.Context, openID string, text string) error
	SendImage(ctx context.Context, openID string, localPath string) error
	SendCard(ctx context.Context, openID string, cardID string) error
}

type sdkMessageSender struct {
	client *lark.Client
	appID  string
	guide  *permissionGuideLimiter
}

// newSDKMessageSender 创建基于飞书 REST client 的消息发送器。
func newSDKMessageSender(client *lark.Client, appID string) messageSender {
	return &sdkMessageSender{client: client, appID: appID, guide: newPermissionGuideLimiter(appID)}
}

// SendText 通过 im.message.create 发送飞书文本消息。
func (s *sdkMessageSender) SendText(ctx context.Context, openID string, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	return s.createMessage(ctx, openID, larkim.MsgTypeText, string(content))
}

// SendImage 上传本地图片并发送 image 消息。
func (s *sdkMessageSender) SendImage(ctx context.Context, openID string, localPath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	imageReq := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType(larkim.CreateImageImageTypeMessage).
			Image(file).
			Build()).
		Build()
	imageResp, err := s.client.Im.Image.Create(ctx, imageReq)
	if err != nil {
		return err
	}
	if !imageResp.Success() || imageResp.Data == nil || imageResp.Data.ImageKey == nil {
		return s.apiError(imageResp.Code, imageResp.Msg)
	}
	content, err := json.Marshal(map[string]string{"image_key": *imageResp.Data.ImageKey})
	if err != nil {
		return err
	}
	return s.createMessage(ctx, openID, larkim.MsgTypeImage, string(content))
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
func (s *sdkMessageSender) createMessage(ctx context.Context, openID string, msgType string, content string) error {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeOpenId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(openID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return s.apiError(resp.Code, resp.Msg)
	}
	return nil
}

// apiError 统一处理飞书 API 错误，并对权限引导日志做冷却。
func (s *sdkMessageSender) apiError(code int, msg string) error {
	if guide, ok := s.guide.MessageForCode(code); ok {
		log.Printf("[feishu] %s", guide)
	}
	return formatFeishuAPIError(s.appID, code, msg)
}

// Replier 实现飞书平台的统一回复接口。
type Replier struct {
	sender       messageSender
	cardKit      cardKitClient
	openID       string
	typingMu     sync.Mutex
	typingStream platform.Stream
}

// NewReplier 创建飞书回复器。
func NewReplier(sender messageSender, openID string, cardKitClients ...cardKitClient) *Replier {
	var cardKit cardKitClient
	if len(cardKitClients) > 0 {
		cardKit = cardKitClients[0]
	}
	return &Replier{sender: sender, cardKit: cardKit, openID: openID}
}

// Capabilities 返回飞书回复器能力。
func (r *Replier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Typing: true, Image: true, File: true, Card: true, Streaming: true, Buttons: true, LongText: false}
}

// SendText 拆分超长文本并逐条发送。
func (r *Replier) SendText(ctx context.Context, text string) error {
	for _, chunk := range splitFeishuText(text) {
		if err := r.sender.SendText(ctx, r.openID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendImage 上传并发送本地图片。
func (r *Replier) SendImage(ctx context.Context, localPath string) error {
	return r.sender.SendImage(ctx, r.openID, localPath)
}

// Typing 使用 CardKit thinking 卡片表达处理中状态，关闭时更新为结束态。
func (r *Replier) Typing(ctx context.Context, on bool) error {
	if r.cardKit == nil {
		return nil
	}
	r.typingMu.Lock()
	defer r.typingMu.Unlock()
	if on {
		if r.typingStream != nil {
			return nil
		}
		stream, err := r.openCardKitStream(ctx, platform.StreamOptions{
			Title:          "WeClaw",
			InitialContent: "正在处理，请稍候。",
		})
		if err != nil {
			return err
		}
		r.typingStream = stream
		return nil
	}
	if r.typingStream == nil {
		return nil
	}
	err := r.typingStream.Complete(ctx, "任务已结束。")
	r.typingStream = nil
	return err
}

// OpenStream 优先创建 CardKit 流式卡片；测试或未配置 CardKit 时降级为最终态文本。
func (r *Replier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	if r.cardKit != nil {
		return r.openCardKitStream(ctx, opts)
	}
	return &textFinalStream{reply: r}, nil
}

// AskChoices 优先发送飞书按钮卡片；测试或未配置 CardKit 时降级为编号文本。
func (r *Replier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	if r.cardKit != nil {
		conv := platform.IncomingMessage{Platform: platform.PlatformFeishu, UserID: r.openID}.ConversationKey()
		cardJSON, err := buildChoiceCard(prompt, choices, conv)
		if err != nil {
			return err
		}
		cardID, err := r.cardKit.CreateCard(ctx, cardJSON)
		if err != nil {
			return err
		}
		return r.sender.SendCard(ctx, r.openID, cardID)
	}
	var lines []string
	if strings.TrimSpace(prompt) != "" {
		lines = append(lines, prompt)
	}
	for _, choice := range choices {
		lines = append(lines, fmt.Sprintf("%s. %s", choice.ID, choice.Label))
	}
	return r.SendText(ctx, strings.Join(lines, "\n"))
}

// textFinalStream 是 CardKit 接入前的安全降级流，只发送最终态。
type textFinalStream struct {
	reply *Replier
}

// Update 在文本降级流里不发送中间态。
func (s *textFinalStream) Update(ctx context.Context, content string) error {
	return nil
}

// Complete 发送最终完整内容。
func (s *textFinalStream) Complete(ctx context.Context, finalContent string) error {
	return s.reply.SendText(ctx, finalContent)
}

// Fail 发送失败文本。
func (s *textFinalStream) Fail(ctx context.Context, errText string) error {
	return s.reply.SendText(ctx, errText)
}

// splitFeishuText 按 rune 拆分文本，避免超过飞书单条文本限制。
func splitFeishuText(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	chunks := make([]string, 0, len(runes)/feishuTextChunkRunes+1)
	for len(runes) > feishuTextChunkRunes {
		chunks = append(chunks, string(runes[:feishuTextChunkRunes]))
		runes = runes[feishuTextChunkRunes:]
	}
	chunks = append(chunks, string(runes))
	return chunks
}
