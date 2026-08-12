package agent

import (
	"log"
	"sort"
	"strings"
)

const codexTurnControlReserve = 8

// dispatchToTurnCh 把实时事件同时投递给唯一执行 owner 和所有只读前端观察器。
// 审批与问答只交给一个控制消费者，避免多个消息窗口重复回答同一请求。
func (a *ACPAgent) dispatchToTurnCh(threadID string, evt *codexTurnEvent) bool {
	delivered := a.dispatchToTurnChannels(threadID, evt)
	a.notifyCodexThreadActivity(threadID, evt)
	return delivered
}

// dispatchDesktopTurnEvent 必须先恢复无人接收的 pending action，再唤醒 follower。
// 否则新 watcher 可能在 actionSeen 清理前完成一次空回放，并永久错过审批。
func (a *ACPAgent) dispatchDesktopTurnEvent(threadID string, evt *codexTurnEvent) bool {
	delivered := a.dispatchToTurnChannels(threadID, evt)
	if !delivered && a.desktopRuntime != nil {
		a.desktopRuntime.abandonTurnEvent(threadID, evt)
	}
	a.notifyCodexThreadActivity(threadID, evt)
	return delivered
}

func (a *ACPAgent) dispatchToTurnChannels(threadID string, evt *codexTurnEvent) bool {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	owner, ownerOK := a.turnCh[threadID]
	if !ownerOK {
		owner, ownerOK = a.singleActiveTurnChannel(threadID, evt)
	}
	observers := a.turnObserverMailboxesLocked(threadID)
	if isCodexTurnInteractionEvent(evt) {
		if ownerOK {
			return dispatchCodexTurnControlEvent(owner, evt)
		}
		if len(observers) == 0 {
			return false
		}
		return observers[0].enqueue(evt)
	}
	delivered := false
	if ownerOK {
		delivered = dispatchCodexTurnEvent(owner, evt) || delivered
	}
	for _, observer := range observers {
		delivered = observer.enqueue(evt) || delivered
	}
	return delivered
}

func dispatchCodexTurnEvent(ch chan *codexTurnEvent, evt *codexTurnEvent) bool {
	if isCodexTurnControlEvent(evt) {
		return dispatchCodexTurnControlEvent(ch, evt)
	}
	limit := cap(ch)
	if limit > codexTurnControlReserve {
		limit -= codexTurnControlReserve
	}
	if len(ch) >= limit {
		return false
	}
	select {
	case ch <- evt:
		return true
	default:
		return false
	}
}

func (a *ACPAgent) turnObserverMailboxesLocked(threadID string) []*codexTurnObserverMailbox {
	registered := a.turnObservers[threadID]
	if len(registered) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(registered))
	for id := range registered {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	mailboxes := make([]*codexTurnObserverMailbox, 0, len(ids))
	for _, id := range ids {
		mailboxes = append(mailboxes, registered[id])
	}
	return mailboxes
}

func (a *ACPAgent) notifyCodexThreadActivity(threadID string, evt *codexTurnEvent) {
	if strings.TrimSpace(threadID) == "" || !isCodexFollowerWakeEvent(evt) {
		return
	}
	a.codexThreadActivityMu.RLock()
	handler := a.codexThreadActivityHandler
	a.codexThreadActivityMu.RUnlock()
	if handler != nil {
		handler(threadID)
	}
}

func isCodexTurnInteractionEvent(evt *codexTurnEvent) bool {
	return evt != nil && (evt.Approval != nil || evt.UserInput != nil)
}

func isCodexFollowerWakeEvent(evt *codexTurnEvent) bool {
	return evt != nil && (evt.Kind == "started" || isCodexTurnTerminalEvent(evt) ||
		evt.Approval != nil || evt.UserInput != nil)
}

// singleActiveTurnChannel 仅为空路由事件提供单活动通道兜底，明示未知 thread 必须丢弃。
func (a *ACPAgent) singleActiveTurnChannel(threadID string, evt *codexTurnEvent) (chan *codexTurnEvent, bool) {
	if strings.TrimSpace(threadID) != "" {
		if isCodexTurnControlEvent(evt) {
			log.Printf("[acp] dropping turn event for inactive thread (thread=%q, activeTurns=%d, kind=%s)", threadID, len(a.turnCh), evt.Kind)
		}
		return nil, false
	}
	if len(a.turnCh) == 1 {
		for _, ch := range a.turnCh {
			return ch, true
		}
	}
	if len(a.turnCh) > 1 {
		log.Printf("[acp] dropping turn event without routable thread (thread=%q, activeTurns=%d, kind=%s)", threadID, len(a.turnCh), evt.Kind)
	}
	return nil, false
}

func isCodexTurnControlEvent(evt *codexTurnEvent) bool {
	if evt == nil {
		return false
	}
	return evt.Approval != nil || evt.UserInput != nil || evt.Kind == "completed" ||
		evt.Kind == "interrupted" || evt.Kind == "error" || evt.Kind == "started"
}

func dispatchCodexTurnControlEvent(ch chan *codexTurnEvent, evt *codexTurnEvent) bool {
	select {
	case ch <- evt:
		return true
	default:
	}
	select {
	case queued := <-ch:
		if isCodexTurnControlEvent(queued) && !canEvictCodexControlEvent(queued, evt) {
			select {
			case ch <- queued:
			default:
			}
			return false
		}
	default:
		return false
	}
	select {
	case ch <- evt:
		return true
	default:
		return false
	}
}

// canEvictCodexControlEvent 只允许终态淘汰已过时的启动通知，审批和输入事件必须保留。
func canEvictCodexControlEvent(queued *codexTurnEvent, incoming *codexTurnEvent) bool {
	return queued != nil && queued.Kind == "started" && isCodexTurnTerminalEvent(incoming)
}

// isCodexTurnTerminalEvent 标识不可被启动通知阻塞的最终事件。
func isCodexTurnTerminalEvent(evt *codexTurnEvent) bool {
	if evt == nil {
		return false
	}
	return evt.Kind == "completed" || evt.Kind == "interrupted" || evt.Kind == "error"
}
