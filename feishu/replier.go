package feishu

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/google/uuid"
)

const feishuTextChunkRunes = 30000

// Replier 实现飞书平台的统一回复接口。
type Replier struct {
	sender       messageSender
	cardKit      cardKitClient
	accountID    string
	openID       string
	replyToID    string
	taskCards    *taskCardRegistry
	taskCardMu   sync.RWMutex
	taskCardID   string
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

// NewReplierForMessage 创建会回复到原飞书消息 / 话题的回复器。
func NewReplierForMessage(sender messageSender, openID string, replyToID string, cardKitClients ...cardKitClient) *Replier {
	reply := NewReplier(sender, openID, cardKitClients...)
	reply.replyToID = strings.TrimSpace(replyToID)
	return reply
}

func newReplierWithTaskCards(sender messageSender, openID string, cardKit cardKitClient, cards *taskCardRegistry) *Replier {
	return &Replier{sender: sender, cardKit: cardKit, openID: openID, taskCards: cards}
}

func (r *Replier) withDeliveryAccount(accountID string) *Replier {
	r.accountID = strings.TrimSpace(accountID)
	return r
}

// Capabilities 返回飞书回复器能力。
func (r *Replier) Capabilities() platform.Capabilities {
	streaming := r.cardKit != nil
	return platform.Capabilities{
		Text: true, Typing: true, Image: true, File: true, Card: true,
		Streaming: streaming, Buttons: true, LongText: false,
		StreamCompletionNotification: streaming,
	}
}

// SendText 拆分超长文本并逐条发送。
func (r *Replier) SendText(ctx context.Context, text string) error {
	for _, chunk := range splitFeishuText(text) {
		if r.replyToID != "" {
			if err := r.sender.ReplyText(ctx, r.replyToID, chunk); err != nil {
				return err
			}
			continue
		}
		if err := r.sender.SendText(ctx, r.openID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendTextIdempotent 为每个文本分片派生稳定 UUID，供 outbox 安全重试。
func (r *Replier) SendTextIdempotent(ctx context.Context, text string, deliveryKey string) error {
	chunks := splitFeishuText(text)
	sender, idempotent := r.sender.(idempotentMessageSender)
	if !idempotent {
		return platform.ErrUnsupported
	}
	for index, chunk := range chunks {
		operationID := terminalTextOperationID(deliveryKey, index)
		if r.replyToID != "" {
			if err := sender.ReplyTextIdempotent(ctx, r.replyToID, chunk, operationID); err != nil {
				return err
			}
			continue
		}
		if err := sender.SendTextIdempotent(ctx, r.openID, chunk, operationID); err != nil {
			return err
		}
	}
	return nil
}

func terminalTextOperationID(deliveryKey string, index int) string {
	seed := fmt.Sprintf("%s:%d", strings.TrimSpace(deliveryKey), index)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

// DeliveryRoute 返回 outbox 可持久化的最小飞书路由。
func (r *Replier) DeliveryRoute() platform.DeliveryRoute {
	return platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: r.accountID,
		ChatID: r.openID, ReplyToID: r.replyToID,
	}
}

// DeliverTerminal 重放已经持久化的 CardKit 终态操作。
func (r *Replier) DeliverTerminal(ctx context.Context, checkpoint platform.TerminalCheckpoint) error {
	return deliverFeishuTerminalCheckpoint(ctx, r.cardKit, checkpoint)
}

// SendImage 上传并发送本地图片。
func (r *Replier) SendImage(ctx context.Context, localPath string) error {
	if r.replyToID != "" {
		return r.sender.ReplyImage(ctx, r.replyToID, localPath)
	}
	return r.sender.SendImage(ctx, r.openID, localPath)
}

// SendFile 上传并发送本地文件。
func (r *Replier) SendFile(ctx context.Context, localPath string) error {
	if r.replyToID != "" {
		return r.sender.ReplyFile(ctx, r.replyToID, localPath)
	}
	return r.sender.SendFile(ctx, r.openID, localPath)
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
		return r.openTaskCardKitStream(ctx, opts)
	}
	return &textFinalStream{reply: r}, nil
}

// AskChoices 优先发送飞书按钮卡片；测试或未配置 CardKit 时降级为编号文本。
func (r *Replier) AskChoices(ctx context.Context, prompt string, choices []platform.Choice) error {
	if r.cardKit != nil {
		conv := platform.IncomingMessage{Platform: platform.PlatformFeishu, UserID: r.openID}.ConversationKey()
		taskCardID := r.CurrentTaskCardID()
		choices = attachTaskCardID(choices, taskCardID)
		targetOpenID := approvalTargetOpenID(choices, r.openID)
		panelReq := approvalPanelRequest{Prompt: prompt, Choices: choices, Conv: conv, TaskCard: taskCardID}
		if handled, err := r.askApprovalPanel(ctx, panelReq, targetOpenID); handled || err != nil {
			return err
		}
		cardJSON, err := buildChoiceCard(prompt, choices, conv)
		if err != nil {
			return err
		}
		cardID, err := r.cardKit.CreateCard(ctx, cardJSON)
		if err != nil {
			return err
		}
		return r.sendCard(ctx, targetOpenID, cardID)
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

// askApprovalPanel 将同一任务内的多个审批合并到一张面板卡片，避免聊天里刷出多张审批卡。
func (r *Replier) askApprovalPanel(ctx context.Context, req approvalPanelRequest, targetOpenID string) (bool, error) {
	item, ok := newApprovalPanelItem(req)
	if !ok || r.taskCards == nil {
		return false, nil
	}
	snapshot, ok := r.taskCards.upsertApprovalPanelItem(req.TaskCard, item)
	if !ok {
		return false, nil
	}
	cardJSON, err := buildApprovalPanelCardJSON(snapshot)
	if err != nil {
		return false, err
	}
	if snapshot.CardID == "" {
		return r.createApprovalPanel(ctx, req.TaskCard, targetOpenID, cardJSON)
	}
	if err := r.cardKit.UpdateCard(ctx, snapshot.CardID, cardJSON, snapshot.Seq); err != nil {
		r.taskCards.removeApprovalPanelItem(req.TaskCard, item.Key)
		return false, nil
	}
	return true, nil
}

func (r *Replier) createApprovalPanel(ctx context.Context, taskCardID string, targetOpenID string, cardJSON string) (bool, error) {
	cardID, err := r.cardKit.CreateCard(ctx, cardJSON)
	if err != nil {
		return false, nil
	}
	if _, ok := r.taskCards.bindApprovalPanelCard(taskCardID, cardID); !ok {
		return false, nil
	}
	return true, r.sendCard(ctx, targetOpenID, cardID)
}

func (r *Replier) sendCard(ctx context.Context, targetOpenID string, cardID string) error {
	if r.replyToID != "" && targetOpenID == r.openID {
		return r.sender.ReplyCard(ctx, r.replyToID, cardID)
	}
	return r.sender.SendCard(ctx, targetOpenID, cardID)
}

// CurrentTaskCardID 返回当前任务流卡片 ID，供审批按钮回写主任务卡片。
func (r *Replier) CurrentTaskCardID() string {
	r.taskCardMu.RLock()
	defer r.taskCardMu.RUnlock()
	return r.taskCardID
}

func (r *Replier) setCurrentTaskCardID(cardID string) {
	r.taskCardMu.Lock()
	r.taskCardID = strings.TrimSpace(cardID)
	r.taskCardMu.Unlock()
}

// BindTaskCard 把后续审批和结构化问答关联到当前任务卡。
func (r *Replier) BindTaskCard(cardID string) {
	r.setCurrentTaskCardID(cardID)
}

func attachTaskCardID(choices []platform.Choice, cardID string) []platform.Choice {
	cardID = strings.TrimSpace(cardID)
	if cardID == "" {
		return choices
	}
	out := make([]platform.Choice, 0, len(choices))
	for _, choice := range choices {
		metadata := make(map[string]string, len(choice.Metadata)+1)
		for key, value := range choice.Metadata {
			metadata[key] = value
		}
		metadata["task_card_id"] = cardID
		choice.Metadata = metadata
		out = append(out, choice)
	}
	return out
}

// approvalTargetOpenID 只把 Codex 审批发送给发起人，普通选择卡继续使用原会话目标。
func approvalTargetOpenID(choices []platform.Choice, fallback string) string {
	for _, choice := range choices {
		if strings.TrimSpace(choice.Metadata["approval_key"]) == "" {
			continue
		}
		if owner := strings.TrimSpace(choice.Metadata[approvalOwnerValueKey]); owner != "" {
			return owner
		}
	}
	return fallback
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
