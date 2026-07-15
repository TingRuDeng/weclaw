package ilink

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
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
	queues          map[string]chan queuedWeixinMessage
	aggregateWindow time.Duration
}

// NewMonitor creates a new long-poll monitor.
func NewMonitor(client *Client, handler MessageHandler) (*Monitor, error) {
	home, err := config.DataDir()
	if err != nil {
		return nil, err
	}
	accountID := NormalizeAccountID(client.BotID())
	bufPath := filepath.Join(home, "accounts", accountID+".sync.json")

	m := &Monitor{
		client:       client,
		handler:      handler,
		bufPath:      bufPath,
		lastActivity: time.Now(),
		queues:       make(map[string]chan queuedWeixinMessage),
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
				log.Printf("[monitor] WARNING: %d consecutive failures. If this persists, run `weclaw wechat login` to re-authenticate.", maxConsecutiveFailures)
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
			backoff := m.recoverExpiredSession()
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

		if !m.processUpdateResponse(ctx, resp) {
			return ctx.Err()
		}
	}
}

// recoverExpiredSession 根据清空前的游标状态选择恢复退避，并持久化游标重置。
func (m *Monitor) recoverExpiredSession() time.Duration {
	if m.getUpdatesBuf == "" {
		log.Printf("[monitor] WARNING: WeChat session expired and cannot be auto-recovered. Run `weclaw wechat login` to re-authenticate.")
		return fatalSessionBackoff
	}
	log.Printf("[monitor] session expired, resetting sync buf")
	m.getUpdatesBuf = ""
	m.saveBuf()
	return sessionExpiredBackoff
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
