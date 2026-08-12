package agent

import "sync"

// codexTurnObserverMailbox 为每个外部前端保留独立 FIFO。平台卡片更新较慢时，
// 只会让该 route 的邮箱积压，不会丢弃自然语言进度或阻塞其他 route。
type codexTurnObserverMailbox struct {
	mu       sync.Mutex
	target   chan *codexTurnEvent
	queue    []*codexTurnEvent
	inFlight *codexTurnEvent
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	closed   bool
}

func newCodexTurnObserverMailbox(target chan *codexTurnEvent) *codexTurnObserverMailbox {
	mailbox := &codexTurnObserverMailbox{
		target: target, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go mailbox.run()
	return mailbox
}

func (m *codexTurnObserverMailbox) enqueue(event *codexTurnEvent) bool {
	if m == nil || event == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	// 快速路径保留原有低延迟，也让已有的缓冲通道测试保持同步可观测。
	if len(m.queue) == 0 && m.inFlight == nil {
		select {
		case m.target <- event:
			return true
		default:
		}
	}
	m.queue = append(m.queue, event)
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return true
}

func (m *codexTurnObserverMailbox) run() {
	defer close(m.done)
	for {
		m.mu.Lock()
		if len(m.queue) == 0 {
			m.mu.Unlock()
			select {
			case <-m.wake:
				continue
			case <-m.stop:
				return
			}
		}
		event := m.queue[0]
		m.queue = m.queue[1:]
		m.inFlight = event
		m.mu.Unlock()

		select {
		case m.target <- event:
			m.mu.Lock()
			m.inFlight = nil
			m.mu.Unlock()
		case <-m.stop:
			return
		}
	}
}

// stopAndDrain 停止邮箱并按原顺序返回尚未被消费者取得的事件。
func (m *codexTurnObserverMailbox) stopAndDrain() []*codexTurnEvent {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.stop)
	}
	m.mu.Unlock()
	<-m.done

	var drained []*codexTurnEvent
	for {
		select {
		case event := <-m.target:
			drained = append(drained, event)
		default:
			m.mu.Lock()
			if m.inFlight != nil {
				drained = append(drained, m.inFlight)
				m.inFlight = nil
			}
			drained = append(drained, m.queue...)
			m.queue = nil
			m.mu.Unlock()
			return drained
		}
	}
}
