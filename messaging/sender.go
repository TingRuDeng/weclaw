package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/google/uuid"
)

const defaultTextReplyChunkRunes = 1800

type textReplyChunkLimitKey struct{}

// NewClientID generates a new unique client ID for message correlation.
func NewClientID() string {
	return uuid.New().String()
}

// SendTypingState sends a typing indicator to a user via the iLink sendtyping API.
// It first fetches a typing_ticket via getconfig, then sends the typing status.
func SendTypingState(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	return SendTypingStatus(ctx, client, userID, contextToken, ilink.TypingStatusTyping)
}

// SendTypingCancel sends a typing cancel signal to clear typing status on WeChat.
func SendTypingCancel(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	return SendTypingStatus(ctx, client, userID, contextToken, ilink.TypingStatusCancel)
}

// SendTypingStatus sends typing status (typing/cancel) to a user via iLink sendtyping API.
func SendTypingStatus(ctx context.Context, client *ilink.Client, userID, contextToken string, status int) error {
	// Get typing ticket
	configResp, err := client.GetConfig(ctx, userID, contextToken)
	if err != nil {
		return fmt.Errorf("get config for typing: %w", err)
	}
	if configResp.TypingTicket == "" {
		return fmt.Errorf("no typing_ticket returned from getconfig")
	}

	// Send typing
	if err := client.SendTyping(ctx, userID, configResp.TypingTicket, status); err != nil {
		return fmt.Errorf("send typing: %w", err)
	}

	log.Printf("[sender] sent typing status=%d to %s", status, userID)
	return nil
}

// SendTextReply sends a text reply to a user through the iLink API.
// If clientID is empty, a new one is generated.
func SendTextReply(ctx context.Context, client *ilink.Client, toUserID, text, contextToken, clientID string) error {
	if clientID == "" {
		clientID = NewClientID()
	}

	// Convert markdown to plain text for WeChat display
	plainText := MarkdownToPlainText(text)
	return sendPlainTextReply(ctx, client, toUserID, plainText, contextToken, clientID)
}

// SendTextReplyChunks 将过长回复按自然边界拆成多条微信消息，避免单条消息过长。
func SendTextReplyChunks(ctx context.Context, client *ilink.Client, toUserID, text, contextToken, clientID string, maxRunes int) error {
	plainText := MarkdownToPlainText(text)
	chunks := splitTextReplyChunks(plainText, maxRunes)
	for i, chunk := range chunks {
		chunkClientID := clientID
		if i > 0 || chunkClientID == "" {
			chunkClientID = NewClientID()
		}
		if err := sendPlainTextReply(ctx, client, toUserID, chunk, contextToken, chunkClientID); err != nil {
			return err
		}
	}
	return nil
}

// sendPlainTextReply 发送已经转换为纯文本的微信消息，避免分段时重复做 markdown 转换。
func sendPlainTextReply(ctx context.Context, client *ilink.Client, toUserID, plainText, contextToken, clientID string) error {
	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     clientID,
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{
					Type: ilink.ItemTypeText,
					TextItem: &ilink.TextItem{
						Text: plainText,
					},
				},
			},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	if resp.Ret != 0 {
		return fmt.Errorf("send message failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}

	log.Printf("[sender] sent reply to %s: %q", toUserID, truncate(plainText, 50))
	return nil
}

// splitTextReplyChunks 优先按换行拆分，单行过长时再按 rune 硬拆，防止截断中文字符。
func splitTextReplyChunks(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = defaultTextReplyChunkRunes
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return []string{text}
	}

	var chunks []string
	for _, block := range strings.Split(text, "\n") {
		chunks = appendTextBlock(chunks, block, maxRunes)
	}
	return chunks
}

// appendTextBlock 将一个文本行追加到分段结果，必要时拆成多个块。
func appendTextBlock(chunks []string, block string, maxRunes int) []string {
	if block == "" {
		return appendChunkWithSeparator(chunks, "", maxRunes)
	}
	for utf8.RuneCountInString(block) > maxRunes {
		part, rest := splitRunesAt(block, maxRunes)
		chunks = appendChunkWithSeparator(chunks, part, maxRunes)
		block = rest
	}
	return appendChunkWithSeparator(chunks, block, maxRunes)
}

// appendChunkWithSeparator 在不超过上限时保留原有换行关系追加内容。
func appendChunkWithSeparator(chunks []string, part string, maxRunes int) []string {
	if len(chunks) == 0 {
		return []string{part}
	}
	last := chunks[len(chunks)-1]
	candidate := last + "\n" + part
	if utf8.RuneCountInString(candidate) <= maxRunes {
		chunks[len(chunks)-1] = candidate
		return chunks
	}
	return append(chunks, part)
}

// splitRunesAt 按 rune 拆分字符串，避免 UTF-8 字节切断导致乱码。
func splitRunesAt(text string, limit int) (string, string) {
	runes := []rune(text)
	return string(runes[:limit]), string(runes[limit:])
}

// textReplyChunkLimit 返回当前上下文的分段上限，默认使用微信友好的保守长度。
func textReplyChunkLimit(ctx context.Context) int {
	if limit, ok := ctx.Value(textReplyChunkLimitKey{}).(int); ok && limit > 0 {
		return limit
	}
	return defaultTextReplyChunkRunes
}

// ctxWithChunkLimit 仅用于测试注入较小上限，生产路径使用默认长度。
func ctxWithChunkLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, textReplyChunkLimitKey{}, limit)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
