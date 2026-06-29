package feishu

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
)

const feishuTextChunkRunes = 30000

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
