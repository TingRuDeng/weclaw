package ilink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxConsecutiveFailures = 5
	initialBackoff         = 3 * time.Second
	maxBackoff             = 60 * time.Second
	sessionExpiredBackoff  = 5 * time.Second
	errCodeSessionExpired  = -14
)

// MessageHandler is called for each received message.
type MessageHandler func(ctx context.Context, client *Client, msg WeixinMessage)

// Monitor manages the long-poll loop for receiving messages.
type Monitor struct {
	client        *Client
	handler       MessageHandler
	getUpdatesBuf string
	bufPath       string
	failures      int
	lastActivity  time.Time
	queuesMu      sync.Mutex
	queues        map[string]chan WeixinMessage
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
		m.lastActivity = time.Now()

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
			select {
			case <-time.After(sessionExpiredBackoff):
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
			m.handler(ctx, m.client, msg)
		}
	}
}

// calcBackoff returns an exponential backoff duration capped at maxBackoff.
func (m *Monitor) calcBackoff() time.Duration {
	d := initialBackoff
	for i := 1; i < m.failures; i++ {
		d *= 2
		if d > maxBackoff {
			return maxBackoff
		}
	}
	return d
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
