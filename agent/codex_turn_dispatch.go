package agent

import (
	"log"
	"strings"
)

const codexTurnControlReserve = 8

// dispatchToTurnCh 为控制事件保留容量，普通进度拥塞时可以丢弃但终态不能丢。
func (a *ACPAgent) dispatchToTurnCh(threadID string, evt *codexTurnEvent) bool {
	a.notifyMu.Lock()
	ch, ok := a.turnCh[threadID]
	if !ok {
		ch, ok = a.singleActiveTurnChannel(threadID, evt)
	}
	a.notifyMu.Unlock()
	if !ok {
		return false
	}
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

// singleActiveTurnChannel 仅为空路由事件提供单活动通道兜底，明示未知 thread 必须丢弃。
func (a *ACPAgent) singleActiveTurnChannel(threadID string, evt *codexTurnEvent) (chan *codexTurnEvent, bool) {
	if strings.TrimSpace(threadID) != "" {
		log.Printf("[acp] dropping turn event for inactive thread (thread=%q, activeTurns=%d, kind=%s)", threadID, len(a.turnCh), evt.Kind)
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
