package agent

import (
	"sort"
	"strings"
)

// failAppServerActiveTurns 仅中断 app-server 观察流，保留 Desktop watcher；
// 服务端 turn 是否终态必须由后续 rollout/thread 状态确认。
func (a *ACPAgent) failAppServerActiveTurns(reason string) {
	type turnTarget struct {
		channel chan *codexTurnEvent
		turnID  string
	}
	a.notifyMu.Lock()
	targets := make([]turnTarget, 0, len(a.turnCh))
	for threadID, channel := range a.turnCh {
		turnID := ""
		if a.codexOwners != nil {
			binding, ok := a.codexOwners.threadBinding(threadID)
			if ok && binding.Runtime == CodexRuntimeDesktop {
				continue
			}
			if ok {
				turnID = binding.State.ActiveTurnID
			}
		}
		targets = append(targets, turnTarget{channel: channel, turnID: turnID})
	}
	a.notifyMu.Unlock()
	for _, target := range targets {
		dispatchCodexTurnControlEvent(target.channel, &codexTurnEvent{
			Kind: "interrupted", TurnID: target.turnID, Text: reason,
		})
	}
}

// registerTurnChannel 原子注册 thread/session 的事件所有者，已有任务时拒绝覆盖。
func (a *ACPAgent) registerTurnChannel(key string, ch chan *codexTurnEvent) bool {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	if _, exists := a.turnCh[key]; exists {
		return false
	}
	a.turnCh[key] = ch
	return true
}

// unregisterTurnChannel 只允许当前所有者删除注册，避免旧任务清理后来者。
func (a *ACPAgent) unregisterTurnChannel(key string, owner chan *codexTurnEvent) {
	a.notifyMu.Lock()
	if a.turnCh[key] != owner {
		a.notifyMu.Unlock()
		return
	}
	delete(a.turnCh, key)
	interactions := drainCodexTurnInteractions(owner)
	a.notifyMu.Unlock()
	for _, event := range interactions {
		if !a.dispatchToTurnChannels(key, event) {
			a.abandonCodexTurnEvent(key, event)
		}
	}
	a.redispatchPendingCodexInteractions(key)
}

// registerTurnObserver 登记只读 frontend 观察器，不占用 app-server 的唯一执行 owner。
func (a *ACPAgent) registerTurnObserver(key string, ch chan *codexTurnEvent) uint64 {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	if a.turnObservers == nil {
		a.turnObservers = make(map[string]map[uint64]*codexTurnObserverMailbox)
	}
	a.turnObserverNext++
	id := a.turnObserverNext
	if a.turnObservers[key] == nil {
		a.turnObservers[key] = make(map[uint64]*codexTurnObserverMailbox)
	}
	a.turnObservers[key][id] = newCodexTurnObserverMailbox(ch)
	return id
}

func (a *ACPAgent) unregisterTurnObserver(key string, id uint64, owner chan *codexTurnEvent) {
	a.notifyMu.Lock()
	observers := a.turnObservers[key]
	mailbox := observers[id]
	if mailbox == nil || mailbox.target != owner {
		a.notifyMu.Unlock()
		return
	}
	delete(observers, id)
	if len(observers) == 0 {
		delete(a.turnObservers, key)
	}
	a.notifyMu.Unlock()
	events := mailbox.stopAndDrain()
	interactions := codexTurnInteractions(events)
	for _, event := range interactions {
		if !a.dispatchToTurnChannels(key, event) {
			a.abandonCodexTurnEvent(key, event)
		}
	}
	a.redispatchPendingCodexInteractions(key)
}

func codexTurnInteractions(events []*codexTurnEvent) []*codexTurnEvent {
	interactions := make([]*codexTurnEvent, 0, len(events))
	for _, event := range events {
		if isCodexTurnInteractionEvent(event) {
			interactions = append(interactions, event)
		}
	}
	return interactions
}

func drainCodexTurnInteractions(channel chan *codexTurnEvent) []*codexTurnEvent {
	var interactions []*codexTurnEvent
	for {
		select {
		case event := <-channel:
			if isCodexTurnInteractionEvent(event) {
				interactions = append(interactions, event)
			}
		default:
			return interactions
		}
	}
}

func (a *ACPAgent) abandonCodexTurnEvent(threadID string, event *codexTurnEvent) {
	if event == nil || !isCodexTurnInteractionEvent(event) {
		return
	}
	if event.DesktopRevision > 0 && a.desktopRuntime != nil {
		a.desktopRuntime.abandonTurnEvent(threadID, event)
		return
	}
	a.rememberPendingCodexInteraction(threadID, event)
}

// redispatchPendingCodexInteractions 在 owner 或 observer 退出后立即把待处理交互
// 交给仍在线的下一个消费者；无消费者时继续保留为可重放状态。
func (a *ACPAgent) redispatchPendingCodexInteractions(threadID string) {
	var events []*codexTurnEvent
	if a.desktopRuntime != nil {
		events = append(events, a.desktopRuntime.replayPendingActionEvents(threadID)...)
	}
	events = append(events, a.claimPendingCodexInteractions(threadID)...)
	for _, event := range events {
		if a.dispatchToTurnChannels(threadID, event) {
			continue
		}
		a.abandonCodexTurnEvent(threadID, event)
	}
}

func (a *ACPAgent) rememberPendingCodexInteraction(threadID string, event *codexTurnEvent) {
	threadID = strings.TrimSpace(threadID)
	requestID := codexInteractionID(event)
	if a.protocol != protocolCodexAppServer || threadID == "" || requestID == "" {
		return
	}
	a.notifyMu.Lock()
	if a.pendingTurnInteractions == nil {
		a.pendingTurnInteractions = make(map[string]map[string]*codexTurnEvent)
	}
	if a.pendingTurnInteractions[threadID] == nil {
		a.pendingTurnInteractions[threadID] = make(map[string]*codexTurnEvent)
	}
	a.pendingTurnInteractions[threadID][requestID] = event
	a.notifyMu.Unlock()
	a.notifyCodexThreadActivity(threadID, event)
}

func (a *ACPAgent) claimPendingCodexInteractions(threadID string) []*codexTurnEvent {
	threadID = strings.TrimSpace(threadID)
	a.notifyMu.Lock()
	pending := a.pendingTurnInteractions[threadID]
	delete(a.pendingTurnInteractions, threadID)
	a.notifyMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	events := make([]*codexTurnEvent, 0, len(ids))
	for _, id := range ids {
		events = append(events, pending[id])
	}
	return events
}

func (a *ACPAgent) forgetPendingCodexInteraction(threadID string, event *codexTurnEvent) {
	threadID = strings.TrimSpace(threadID)
	requestID := codexInteractionID(event)
	if threadID == "" || requestID == "" {
		return
	}
	a.notifyMu.Lock()
	delete(a.pendingTurnInteractions[threadID], requestID)
	if len(a.pendingTurnInteractions[threadID]) == 0 {
		delete(a.pendingTurnInteractions, threadID)
	}
	a.notifyMu.Unlock()
}

// registerLegacySessionChannels 原子注册标准 ACP session 的正文与审批通道。
func (a *ACPAgent) registerLegacySessionChannels(sessionID string, notify chan *sessionUpdate, approval chan *codexTurnEvent) bool {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	if _, exists := a.notifyCh[sessionID]; exists {
		return false
	}
	if _, exists := a.turnCh[sessionID]; exists {
		return false
	}
	a.notifyCh[sessionID] = notify
	a.turnCh[sessionID] = approval
	return true
}

// unregisterLegacySessionChannels 仅清理调用者仍然拥有的标准 ACP session 通道。
func (a *ACPAgent) unregisterLegacySessionChannels(sessionID string, notify chan *sessionUpdate, approval chan *codexTurnEvent) {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	if a.notifyCh[sessionID] == notify {
		delete(a.notifyCh, sessionID)
	}
	if a.turnCh[sessionID] == approval {
		delete(a.turnCh, sessionID)
	}
}
