package feishu

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

// feishuDispatchSequencer 保证同一飞书窗口按 adapter 接收顺序进入业务层。
type feishuDispatchSequencer struct {
	mu    sync.Mutex
	tails map[string]*feishuDispatchNode
}

// feishuDispatchNode 可以在票据放弃执行时重定向到它原本等待的前序节点。
// 后继票据会沿依赖链继续等待，不需要为每个超时票据创建中继 goroutine。
type feishuDispatchNode struct {
	mu       sync.Mutex
	done     chan struct{}
	redirect *feishuDispatchNode
	closed   bool
}

type feishuDispatchTicket struct {
	sequencer *feishuDispatchSequencer
	key       string
	previous  *feishuDispatchNode
	current   *feishuDispatchNode
}

type dispatchWaitOptions struct {
	delay    time.Duration
	notice   func()
	dispatch func()
}

type dispatchRunOutcome uint8

const (
	dispatchRunCompleted dispatchRunOutcome = iota
	dispatchRunWaitCanceled
	dispatchRunExecutionCanceled
)

func newFeishuDispatchSequencer() *feishuDispatchSequencer {
	return &feishuDispatchSequencer{tails: make(map[string]*feishuDispatchNode)}
}

// reserve 在启动异步卡片处理前同步登记位置，消除 goroutine 调度造成的抢跑窗口。
func (s *feishuDispatchSequencer) reserve(key string) feishuDispatchTicket {
	key = strings.TrimSpace(key)
	if s == nil || key == "" {
		return feishuDispatchTicket{}
	}
	s.mu.Lock()
	previous := s.tails[key]
	current := &feishuDispatchNode{done: make(chan struct{})}
	s.tails[key] = current
	s.mu.Unlock()
	return feishuDispatchTicket{sequencer: s, key: key, previous: previous, current: current}
}

// run 等待前序分发完成；超时会释放队列，避免单个卡片命令永久阻塞整个窗口。
func (t feishuDispatchTicket) run(ctx context.Context, dispatch func()) bool {
	return t.runAfterOrderedWait(ctx, t.waitForPrevious, dispatch) == dispatchRunCompleted
}

// runWithWaitNotice 在保持严格顺序的同时，对长时间等待只反馈一次。
func (t feishuDispatchTicket) runWithWaitNotice(ctx context.Context, opts dispatchWaitOptions) dispatchRunOutcome {
	wait := func(waitCtx context.Context) bool {
		return t.waitForPreviousWithNotice(waitCtx, opts.delay, opts.notice)
	}
	return t.runAfterOrderedWait(ctx, wait, opts.dispatch)
}

func (t feishuDispatchTicket) runAfterOrderedWait(ctx context.Context, wait func(context.Context) bool, dispatch func()) dispatchRunOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if !wait(ctx) {
		t.finishPreservingPrevious()
		return dispatchRunWaitCanceled
	}
	if t.sequencer == nil {
		dispatch()
		return dispatchRunCompleted
	}
	done := make(chan struct{})
	go func() {
		defer t.finish()
		defer close(done)
		dispatch()
	}()
	select {
	case <-done:
		return dispatchRunCompleted
	case <-ctx.Done():
		return dispatchRunExecutionCanceled
	}
}

// waitForPreviousWithNotice 只在前序票据持续阻塞时触发一次通知。
func (t feishuDispatchTicket) waitForPreviousWithNotice(ctx context.Context, delay time.Duration, notice func()) bool {
	if t.previous == nil || delay <= 0 {
		return t.waitForPrevious(ctx)
	}
	noticeCtx, cancel := context.WithTimeout(ctx, delay)
	defer cancel()
	if waitForFeishuDispatchNode(noticeCtx, t.previous) {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	if !feishuDispatchNodeComplete(t.previous) && notice != nil {
		notice()
	}
	return t.waitForPrevious(ctx)
}

// runAfterWaitTimeout 仅限制前序等待；超时后仍执行当前消息并建立新的队尾。
func (t feishuDispatchTicket) runAfterWaitTimeout(ctx context.Context, dispatch func()) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	ordered := t.waitForPrevious(ctx)
	if ordered {
		defer t.finish()
	} else {
		defer t.finishPreservingPrevious()
	}
	dispatch()
	return ordered
}

// waitForPrevious 等待同窗口前序票据完成，并明确区分等待超时。
func (t feishuDispatchTicket) waitForPrevious(ctx context.Context) bool {
	return waitForFeishuDispatchNode(ctx, t.previous)
}

func (t feishuDispatchTicket) finish() {
	if t.sequencer == nil {
		return
	}
	t.sequencer.mu.Lock()
	t.current.complete(nil)
	if t.sequencer.tails[t.key] == t.current {
		delete(t.sequencer.tails, t.key)
	}
	t.sequencer.mu.Unlock()
}

// finishPreservingPrevious 把当前票据重定向到原前序操作，保持顺序且不创建中继 goroutine。
func (t feishuDispatchTicket) finishPreservingPrevious() {
	if t.sequencer == nil {
		return
	}
	t.sequencer.mu.Lock()
	t.current.complete(t.previous)
	if t.sequencer.tails[t.key] == t.current {
		pending := pendingFeishuDispatchNode(t.previous)
		if pending == nil {
			delete(t.sequencer.tails, t.key)
		} else {
			t.sequencer.tails[t.key] = pending
		}
	}
	t.sequencer.mu.Unlock()
}

func (n *feishuDispatchNode) complete(redirect *feishuDispatchNode) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	n.redirect = redirect
	n.closed = true
	close(n.done)
}

func (n *feishuDispatchNode) state() (*feishuDispatchNode, <-chan struct{}, bool) {
	if n == nil {
		return nil, nil, true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.redirect, n.done, n.closed
}

func waitForFeishuDispatchNode(ctx context.Context, node *feishuDispatchNode) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	for node != nil {
		redirect, done, closed := node.state()
		if redirect != nil {
			node = redirect
			continue
		}
		if closed {
			return true
		}
		select {
		case <-done:
			// 节点关闭时可能同时被重定向；重新读取状态后再决定是否完成。
			continue
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func feishuDispatchNodeComplete(node *feishuDispatchNode) bool {
	return pendingFeishuDispatchNode(node) == nil
}

// pendingFeishuDispatchNode 返回依赖链中第一个尚未完成的节点。
// 清理 tails 的调用方会持有 sequencer.mu；通知路径只把结果用作瞬时状态提示。
func pendingFeishuDispatchNode(node *feishuDispatchNode) *feishuDispatchNode {
	for node != nil {
		redirect, _, closed := node.state()
		if redirect != nil {
			node = redirect
			continue
		}
		if closed {
			return nil
		}
		return node
	}
	return nil
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
