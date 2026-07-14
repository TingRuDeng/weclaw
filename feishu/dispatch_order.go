package feishu

import (
	"context"
	"strings"
	"sync"

	"github.com/fastclaw-ai/weclaw/platform"
)

// feishuDispatchSequencer 保证同一飞书窗口按 adapter 接收顺序进入业务层。
type feishuDispatchSequencer struct {
	mu    sync.Mutex
	tails map[string]chan struct{}
}

type feishuDispatchTicket struct {
	sequencer *feishuDispatchSequencer
	key       string
	previous  <-chan struct{}
	current   chan struct{}
}

func newFeishuDispatchSequencer() *feishuDispatchSequencer {
	return &feishuDispatchSequencer{tails: make(map[string]chan struct{})}
}

// reserve 在启动异步卡片处理前同步登记位置，消除 goroutine 调度造成的抢跑窗口。
func (s *feishuDispatchSequencer) reserve(key string) feishuDispatchTicket {
	key = strings.TrimSpace(key)
	if s == nil || key == "" {
		return feishuDispatchTicket{}
	}
	s.mu.Lock()
	previous := s.tails[key]
	current := make(chan struct{})
	s.tails[key] = current
	s.mu.Unlock()
	return feishuDispatchTicket{sequencer: s, key: key, previous: previous, current: current}
}

// run 等待前序分发完成；超时会释放队列，避免单个卡片命令永久阻塞整个窗口。
func (t feishuDispatchTicket) run(ctx context.Context, dispatch func()) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.sequencer != nil {
		defer t.finish()
	}
	if t.previous != nil {
		select {
		case <-t.previous:
		case <-ctx.Done():
			return false
		}
	}
	if t.sequencer == nil {
		dispatch()
		return true
	}
	done := make(chan struct{})
	go func() {
		dispatch()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (t feishuDispatchTicket) finish() {
	close(t.current)
	t.sequencer.mu.Lock()
	if t.sequencer.tails[t.key] == t.current {
		delete(t.sequencer.tails, t.key)
	}
	t.sequencer.mu.Unlock()
}

func feishuDispatchKey(msg platform.IncomingMessage) string {
	sessionKey := ""
	if msg.Metadata != nil {
		sessionKey = strings.TrimSpace(msg.Metadata[feishuSessionMetadataKey])
	}
	if sessionKey == "" {
		sessionKey = firstNonEmpty(msg.ChatID, msg.UserID)
	}
	return strings.TrimSpace(msg.AccountID) + "\x00" + sessionKey
}
