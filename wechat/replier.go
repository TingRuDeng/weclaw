package wechat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const defaultTextReplyChunkRunes = 1800
const weclawClientIDPrefix = "weclaw:"

// Replier 封装一次微信会话的回复上下文。
type Replier struct {
	Client       *ilink.Client
	ToUserID     string
	ContextToken string
	ClientID     string
	ChunkRunes   int
	mu           sync.Mutex
	clientIDUsed bool
}

// NewReplier 创建微信回复器。
func NewReplier(client *ilink.Client, toUserID string, contextToken string, clientID string) *Replier {
	return &Replier{
		Client:       client,
		ToUserID:     toUserID,
		ContextToken: contextToken,
		ClientID:     clientID,
		ChunkRunes:   defaultTextReplyChunkRunes,
	}
}

func (r *Replier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Typing: true, Image: true, File: true, LongText: true}
}

func (r *Replier) SendText(ctx context.Context, text string) error {
	plainText := MarkdownToPlainText(text)
	displayText := FormatTextForWeChatDisplay(plainText)
	chunks := splitTextReplyChunks(displayText, r.ChunkRunes)
	clientIDs := r.clientIDsForTextChunks(len(chunks))
	for i, chunk := range chunks {
		if err := r.sendPlainText(ctx, chunk, clientIDs[i]); err != nil {
			return err
		}
	}
	return nil
}

// SendTextIdempotent 使用稳定 client_id 重试同一终态文本分片。
func (r *Replier) SendTextIdempotent(ctx context.Context, text string, deliveryKey string) error {
	plainText := MarkdownToPlainText(text)
	displayText := FormatTextForWeChatDisplay(plainText)
	chunks := splitTextReplyChunks(displayText, r.ChunkRunes)
	for index, chunk := range chunks {
		operationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%d", strings.TrimSpace(deliveryKey), index)))
		if err := r.sendPlainText(ctx, chunk, weclawClientIDPrefix+operationID.String()); err != nil {
			return err
		}
	}
	return nil
}

// DeliveryRoute 返回 outbox 可重建的微信会话路由；context_token 由 adapter 的受保护存储恢复。
func (r *Replier) DeliveryRoute() platform.DeliveryRoute {
	accountID := ""
	if r != nil && r.Client != nil {
		accountID = r.Client.BotID()
	}
	return platform.DeliveryRoute{
		Platform: platform.PlatformWeChat, AccountID: accountID, ChatID: r.ToUserID,
	}
}

func (r *Replier) clientIDsForTextChunks(count int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, count)
	for i := 0; i < count; i++ {
		if i == 0 && r.ClientID != "" && !r.clientIDUsed {
			ids[i] = r.ClientID
			r.clientIDUsed = true
			continue
		}
		ids[i] = NewClientID()
	}
	return ids
}

func (r *Replier) SendImage(ctx context.Context, localPath string) error {
	return r.sendMediaFromPath(ctx, localPath)
}

func (r *Replier) SendFile(ctx context.Context, localPath string) error {
	// sendMediaFromPath 已按内容类型分类为 image/video/file，文件类直接走 file 消息。
	return r.sendMediaFromPath(ctx, localPath)
}

func (r *Replier) Typing(ctx context.Context, on bool) error {
	status := ilink.TypingStatusCancel
	if on {
		status = ilink.TypingStatusTyping
	}
	configResp, err := r.Client.GetConfig(ctx, r.ToUserID, r.ContextToken)
	if err != nil {
		return fmt.Errorf("get config for typing: %w", err)
	}
	if configResp.TypingTicket == "" {
		return fmt.Errorf("no typing_ticket returned from getconfig")
	}
	if err := r.Client.SendTyping(ctx, r.ToUserID, configResp.TypingTicket, status); err != nil {
		return fmt.Errorf("send typing: %w", err)
	}
	return nil
}

func (r *Replier) OpenStream(ctx context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	return &textStream{reply: r}, nil
}

func (r *Replier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	lines := []string{prompt}
	for _, choice := range choices {
		lines = append(lines, strings.TrimSpace(choice.ID)+". "+strings.TrimSpace(choice.Label))
	}
	return r.SendText(ctx, strings.Join(lines, "\n"))
}

func (r *Replier) sendPlainText(ctx context.Context, plainText string, clientID string) error {
	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   r.Client.BotID(),
			ToUserID:     r.ToUserID,
			ClientID:     clientID,
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{{
				Type:     ilink.ItemTypeText,
				TextItem: &ilink.TextItem{Text: plainText},
			}},
			ContextToken: r.ContextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}
	resp, err := r.Client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	if resp.Ret != 0 {
		return fmt.Errorf("send message failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}
	log.Printf("[wechat] sent reply to %s: %q", r.ToUserID, truncate(plainText, 50))
	return nil
}

// NewClientID 生成微信出站消息 client_id。
func NewClientID() string {
	return weclawClientIDPrefix + uuid.New().String()
}

type textStream struct {
	reply  *Replier
	mu     sync.Mutex
	closed bool
}

func (s *textStream) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil
	}
	if err := s.reply.Typing(ctx, true); err != nil {
		return err
	}
	return s.reply.SendText(ctx, content)
}

func (s *textStream) Complete(ctx context.Context, finalContent string) error {
	if !s.beginTerminal() {
		return nil
	}
	if err := s.reply.SendText(ctx, finalContent); err != nil {
		return err
	}
	return s.reply.Typing(ctx, false)
}

func (s *textStream) Fail(ctx context.Context, errText string) error {
	if !s.beginTerminal() {
		return nil
	}
	return s.reply.SendText(ctx, errText)
}

func (s *textStream) beginTerminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

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

func splitRunesAt(text string, limit int) (string, string) {
	runes := []rune(text)
	return string(runes[:limit]), string(runes[limit:])
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
