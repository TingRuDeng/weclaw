package ilink

import (
	"context"
	"sort"
	"strings"
	"time"
)

type queuedWeixinMessage struct {
	message WeixinMessage
	done    chan struct{}
}

// processUpdateResponse 等待本批消息完成分发后再提交 cursor，避免进程退出时丢失已确认消息。
func (m *Monitor) processUpdateResponse(ctx context.Context, resp *GetUpdatesResponse) bool {
	sortMessagesForDispatch(resp.Msgs)
	done := make([]<-chan struct{}, 0, len(resp.Msgs))
	for _, msg := range resp.Msgs {
		ack, ok := m.enqueueMessageWithAck(ctx, msg)
		if !ok {
			return false
		}
		done = append(done, ack)
	}
	for _, ack := range done {
		select {
		case <-ack:
		case <-ctx.Done():
			return false
		}
	}
	if resp.GetUpdatesBuf != "" {
		m.getUpdatesBuf = resp.GetUpdatesBuf
		m.saveBuf()
	}
	return true
}

func sortMessagesForDispatch(messages []WeixinMessage) {
	sort.SliceStable(messages, func(i, j int) bool {
		left, right := messages[i], messages[j]
		if left.Seq != 0 && right.Seq != 0 && left.Seq != right.Seq {
			return left.Seq < right.Seq
		}
		if left.MessageID != 0 && right.MessageID != 0 && left.MessageID != right.MessageID {
			return left.MessageID < right.MessageID
		}
		return false
	})
}

func (m *Monitor) enqueueMessage(ctx context.Context, msg WeixinMessage) {
	_, _ = m.enqueueQueuedMessage(ctx, msg, nil)
}

func (m *Monitor) enqueueMessageWithAck(ctx context.Context, msg WeixinMessage) (<-chan struct{}, bool) {
	done := make(chan struct{})
	return m.enqueueQueuedMessage(ctx, msg, done)
}

func (m *Monitor) enqueueQueuedMessage(ctx context.Context, msg WeixinMessage, done chan struct{}) (<-chan struct{}, bool) {
	key := firstNonEmptyString(msg.FromUserID, msg.ToUserID)
	m.queuesMu.Lock()
	queue := m.queues[key]
	if queue == nil {
		queue = make(chan queuedWeixinMessage, 64)
		m.queues[key] = queue
		go m.runMessageQueue(ctx, key, queue)
	}
	select {
	case queue <- queuedWeixinMessage{message: msg, done: done}:
		m.queuesMu.Unlock()
		return done, true
	case <-ctx.Done():
		m.queuesMu.Unlock()
		return done, false
	}
}

func (m *Monitor) runMessageQueue(ctx context.Context, key string, queue chan queuedWeixinMessage) {
	for {
		select {
		case <-ctx.Done():
			m.removeMessageQueue(key, queue)
			return
		case queued := <-queue:
			m.dispatchQueuedMessage(ctx, queue, queued)
			if m.removeIdleMessageQueue(key, queue) {
				return
			}
		}
	}
}

func (m *Monitor) dispatchQueuedMessage(ctx context.Context, queue <-chan queuedWeixinMessage, first queuedWeixinMessage) {
	if m.aggregateWindow <= 0 || isCommandWeixinMessage(first.message) {
		m.handler(ctx, m.client, first.message)
		completeQueuedMessages([]queuedWeixinMessage{first})
		return
	}
	batch := []queuedWeixinMessage{first}
	timer := time.NewTimer(m.aggregateWindow)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case queued := <-queue:
			if isCommandWeixinMessage(queued.message) {
				m.dispatchQueuedBatch(ctx, batch, &queued)
				return
			}
			batch = append(batch, queued)
		case <-timer.C:
			m.dispatchQueuedBatch(ctx, batch, nil)
			return
		}
	}
}

func (m *Monitor) dispatchQueuedBatch(ctx context.Context, batch []queuedWeixinMessage, command *queuedWeixinMessage) {
	m.handler(ctx, m.client, aggregateQueuedMessages(batch))
	completeQueuedMessages(batch)
	if command != nil {
		m.handler(ctx, m.client, command.message)
		completeQueuedMessages([]queuedWeixinMessage{*command})
	}
}

func aggregateQueuedMessages(batch []queuedWeixinMessage) WeixinMessage {
	messages := make([]WeixinMessage, 0, len(batch))
	for _, queued := range batch {
		messages = append(messages, queued.message)
	}
	return aggregateWeixinMessages(messages)
}

func completeQueuedMessages(batch []queuedWeixinMessage) {
	for _, queued := range batch {
		if queued.done != nil {
			close(queued.done)
		}
	}
}

func (m *Monitor) removeIdleMessageQueue(key string, queue chan queuedWeixinMessage) bool {
	m.queuesMu.Lock()
	defer m.queuesMu.Unlock()
	if len(queue) != 0 || m.queues[key] != queue {
		return false
	}
	delete(m.queues, key)
	return true
}

func (m *Monitor) removeMessageQueue(key string, queue chan queuedWeixinMessage) {
	m.queuesMu.Lock()
	if m.queues[key] == queue {
		delete(m.queues, key)
	}
	m.queuesMu.Unlock()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

func (m *Monitor) SetAggregationWindow(window time.Duration) { m.aggregateWindow = window }

func isCommandWeixinMessage(msg WeixinMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(weixinMessageText(msg)), "/")
}

func aggregateWeixinMessages(messages []WeixinMessage) WeixinMessage {
	if len(messages) == 0 {
		return WeixinMessage{}
	}
	if len(messages) == 1 {
		return messages[0]
	}
	result := messages[len(messages)-1]
	texts := make([]string, 0, len(messages))
	items := make([]MessageItem, 0, len(result.ItemList)+1)
	for _, msg := range messages {
		appendAggregatedMessage(&texts, &items, msg)
	}
	if len(texts) > 0 {
		textItem := MessageItem{Type: ItemTypeText, TextItem: &TextItem{Text: strings.Join(texts, "\n")}}
		items = append([]MessageItem{textItem}, items...)
	}
	result.ItemList = items
	result.ClientID = ""
	return result
}

func appendAggregatedMessage(texts *[]string, items *[]MessageItem, msg WeixinMessage) {
	for _, item := range msg.ItemList {
		switch {
		case item.Type == ItemTypeText && item.TextItem != nil:
			if text := strings.TrimSpace(item.TextItem.Text); text != "" {
				*texts = append(*texts, text)
			}
		case item.Type == ItemTypeVoice && item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "":
			*texts = append(*texts, strings.TrimSpace(item.VoiceItem.Text))
			*items = append(*items, item)
		default:
			*items = append(*items, item)
		}
	}
}

func weixinMessageText(msg WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	for _, item := range msg.ItemList {
		if item.Type == ItemTypeVoice && item.VoiceItem != nil {
			return item.VoiceItem.Text
		}
	}
	return ""
}
