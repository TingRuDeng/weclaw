package agent

import "log"

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

func (a *ACPAgent) singleActiveTurnChannel(threadID string, evt *codexTurnEvent) (chan *codexTurnEvent, bool) {
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
	return evt.Approval != nil || evt.UserInput != nil || evt.Kind == "completed" || evt.Kind == "error" || evt.Kind == "started"
}

func dispatchCodexTurnControlEvent(ch chan *codexTurnEvent, evt *codexTurnEvent) bool {
	select {
	case ch <- evt:
		return true
	default:
	}
	select {
	case queued := <-ch:
		if isCodexTurnControlEvent(queued) {
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
