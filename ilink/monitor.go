package ilink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxConsecutiveFailures = 5
	sessionExpiredBackoff  = 5 * time.Second
	fatalSessionBackoff    = 60 * time.Second
	errCodeSessionExpired  = -14
)

var steppedBackoffs = []time.Duration{
	3 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

// MessageHandler is called for each received message.
type MessageHandler func(ctx context.Context, client *Client, msg WeixinMessage)

// Monitor manages the long-poll loop for receiving messages.
type Monitor struct {
	client          *Client
	handler         MessageHandler
	getUpdatesBuf   string
	bufPath         string
	failures        int
	activityMu      sync.RWMutex
	lastActivity    time.Time
	queuesMu        sync.Mutex
	queues          map[string]chan WeixinMessage
	aggregateWindow time.Duration
}

// NewMonitor creates a new long-poll monitor.
func NewMonitor(client *Client, handler MessageHandler) (*Monitor, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	accountID := NormalizeAccountID(client.BotID())
	bufPath := filepath.Join(home, ".weclaw", "accounts", accountID+".sync.json")

	m := &Monitor{
		client:       client,
		handler:      handler,
		bufPath:      bufPath,
		lastActivity: time.Now(),
		queues:       make(map[string]chan WeixinMessage),
	}
	m.loadBuf()
	return m, nil
}

// Run starts the long-poll loop. It blocks until ctx is cancelled.
// Automatically recovers from errors with exponential backoff.
func (m *Monitor) Run(ctx context.Context) error {
	log.Println("[monitor] starting long-poll loop")

	for {
		select {
		case <-ctx.Done():
			log.Println("[monitor] shutting down")
			return ctx.Err()
		default:
		}

		resp, err := m.client.GetUpdates(ctx, m.getUpdatesBuf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.failures++
			backoff := m.calcBackoff()
			log.Printf("[monitor] GetUpdates error (%d/%d, backoff=%s): %v",
				m.failures, maxConsecutiveFailures, backoff, err)
			if m.failures == maxConsecutiveFailures {
				log.Printf("[monitor] WARNING: %d consecutive failures. If this persists, run `weclaw login` to re-authenticate.", maxConsecutiveFailures)
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Reset failure counter on any successful response
		m.failures = 0
		m.setLastActivity(time.Now())

		// Session expired — reset sync buf and reconnect silently
		if resp.ErrCode == errCodeSessionExpired {
			if m.getUpdatesBuf != "" {
				log.Printf("[monitor] session expired, resetting sync buf")
				m.getUpdatesBuf = ""
				m.saveBuf()
			} else {
				// Sync buf already empty but still getting session expired:
				// the bot token itself has expired. The user needs to re-login.
				log.Printf("[monitor] WARNING: WeChat session expired and cannot be auto-recovered. Run `weclaw login` to re-authenticate.")
			}
			backoff := sessionExpiredBackoff
			if m.getUpdatesBuf == "" {
				backoff = fatalSessionBackoff
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Other server errors
		if resp.Ret != 0 && resp.ErrCode != 0 {
			log.Printf("[monitor] server error: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.ErrCode, resp.ErrMsg)
			continue
		}

		// Update buf for next poll
		if resp.GetUpdatesBuf != "" {
			m.getUpdatesBuf = resp.GetUpdatesBuf
			m.saveBuf()
		}

		sortMessagesForDispatch(resp.Msgs)
		for _, msg := range resp.Msgs {
			m.enqueueMessage(ctx, msg)
		}
	}
}

// LastActivity 返回最近一次成功 GetUpdates 的时间，用于外部看门狗判断长轮询是否卡住。
func (m *Monitor) LastActivity() time.Time {
	m.activityMu.RLock()
	defer m.activityMu.RUnlock()
	return m.lastActivity
}

func (m *Monitor) setLastActivity(t time.Time) {
	m.activityMu.Lock()
	m.lastActivity = t
	m.activityMu.Unlock()
}

func sortMessagesForDispatch(messages []WeixinMessage) {
	sort.SliceStable(messages, func(i, j int) bool {
		left := messages[i]
		right := messages[j]
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
	key := msg.FromUserID
	if key == "" {
		key = msg.ToUserID
	}
	queue := m.messageQueue(ctx, key)
	select {
	case queue <- msg:
	case <-ctx.Done():
	}
}

func (m *Monitor) messageQueue(ctx context.Context, key string) chan WeixinMessage {
	m.queuesMu.Lock()
	defer m.queuesMu.Unlock()
	queue := m.queues[key]
	if queue == nil {
		queue = make(chan WeixinMessage, 64)
		m.queues[key] = queue
		go m.runMessageQueue(ctx, queue)
	}
	return queue
}

func (m *Monitor) runMessageQueue(ctx context.Context, queue <-chan WeixinMessage) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-queue:
			m.dispatchQueuedMessage(ctx, queue, msg)
		}
	}
}

func (m *Monitor) dispatchQueuedMessage(ctx context.Context, queue <-chan WeixinMessage, first WeixinMessage) {
	if m.aggregateWindow <= 0 || isCommandWeixinMessage(first) {
		m.handler(ctx, m.client, first)
		return
	}
	batch := []WeixinMessage{first}
	timer := time.NewTimer(m.aggregateWindow)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-queue:
			if isCommandWeixinMessage(msg) {
				m.handler(ctx, m.client, aggregateWeixinMessages(batch))
				m.handler(ctx, m.client, msg)
				return
			}
			batch = append(batch, msg)
		case <-timer.C:
			m.handler(ctx, m.client, aggregateWeixinMessages(batch))
			return
		}
	}
}

// SetAggregationWindow 设置同一用户连续消息聚合窗口；0 表示关闭聚合。
func (m *Monitor) SetAggregationWindow(window time.Duration) {
	m.aggregateWindow = window
}

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
		for _, item := range msg.ItemList {
			switch {
			case item.Type == ItemTypeText && item.TextItem != nil:
				text := strings.TrimSpace(item.TextItem.Text)
				if text != "" {
					texts = append(texts, text)
				}
			case item.Type == ItemTypeVoice && item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "":
				texts = append(texts, strings.TrimSpace(item.VoiceItem.Text))
				items = append(items, item)
			default:
				items = append(items, item)
			}
		}
	}
	if len(texts) > 0 {
		textItem := MessageItem{Type: ItemTypeText, TextItem: &TextItem{Text: strings.Join(texts, "\n")}}
		items = append([]MessageItem{textItem}, items...)
	}
	result.ItemList = items
	result.ClientID = ""
	return result
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

// calcBackoff 返回固定阶梯退避，避免指数退避在微信短暂抖动时过慢恢复。
func (m *Monitor) calcBackoff() time.Duration {
	index := m.failures - 1
	if index < 0 {
		index = 0
	}
	if index >= len(steppedBackoffs) {
		index = len(steppedBackoffs) - 1
	}
	return steppedBackoffs[index]
}

type syncData struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

func (m *Monitor) loadBuf() {
	data, err := os.ReadFile(m.bufPath)
	if err != nil {
		return
	}
	var s syncData
	if json.Unmarshal(data, &s) == nil && s.GetUpdatesBuf != "" {
		m.getUpdatesBuf = s.GetUpdatesBuf
		log.Printf("[monitor] loaded sync buf from %s", m.bufPath)
	}
}

func (m *Monitor) saveBuf() {
	dir := filepath.Dir(m.bufPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[monitor] failed to create buf dir: %v", err)
		return
	}
	data, _ := json.Marshal(syncData{GetUpdatesBuf: m.getUpdatesBuf})
	if err := os.WriteFile(m.bufPath, data, 0o600); err != nil {
		log.Printf("[monitor] failed to save buf: %v", err)
	}
}

// FormatMessageSummary returns a short description of a message for logging.
func FormatMessageSummary(msg WeixinMessage) string {
	text := ""
	for _, item := range msg.ItemList {
		if item.Type == ItemTypeText && item.TextItem != nil {
			text = item.TextItem.Text
			break
		}
	}
	if len(text) > 50 {
		text = text[:50] + "..."
	}
	return fmt.Sprintf("from=%s type=%d state=%d text=%q", msg.FromUserID, msg.MessageType, msg.MessageState, text)
}
