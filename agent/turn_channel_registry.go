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
	if _, exists := a.turnCh[key]; exists {
		a.notifyMu.Unlock()
		return false
	}
	a.turnCh[key] = ch
	pending := a.pendingCodexInteractionsLocked(key)
	a.notifyMu.Unlock()
	for _, event := range pending {
		dispatchCodexTurnControlEvent(ch, event)
	}
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
	a.notifyMu.Unlock()
	for _, event := range drainCodexTurnInteractions(owner) {
		a.abandonCodexTurnEvent(key, event)
	}
}

// registerTurnObserver 登记只读 frontend 观察器，不占用 app-server 的唯一执行 owner。
func (a *ACPAgent) registerTurnObserver(key string, ch chan *codexTurnEvent) uint64 {
	a.notifyMu.Lock()
	if a.turnObservers == nil {
		a.turnObservers = make(map[string]map[uint64]*codexTurnObserverMailbox)
	}
	a.turnObserverNext++
	id := a.turnObserverNext
	if a.turnObservers[key] == nil {
		a.turnObservers[key] = make(map[uint64]*codexTurnObserverMailbox)
	}
	mailbox := newCodexTurnObserverMailbox(ch)
	a.turnObservers[key][id] = mailbox
	pending := a.pendingCodexInteractionsLocked(key)
	a.notifyMu.Unlock()
	for _, event := range pending {
		mailbox.enqueue(event)
	}
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
	for _, event := range codexTurnInteractions(mailbox.stopAndDrain()) {
		a.abandonCodexTurnEvent(key, event)
	}
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

func (a *ACPAgent) rememberPendingCodexInteraction(threadID string, event *codexTurnEvent) {
	threadID = strings.TrimSpace(threadID)
	requestKey := codexInteractionBrokerKey(event)
	if a.protocol != protocolCodexAppServer || threadID == "" || requestKey == "" {
		return
	}
	a.notifyMu.Lock()
	a.rememberPendingCodexInteractionLocked(threadID, event)
	a.notifyMu.Unlock()
	a.notifyCodexThreadActivity(threadID, event)
}

func (a *ACPAgent) claimPendingCodexInteractions(threadID string) []*codexTurnEvent {
	threadID = strings.TrimSpace(threadID)
	a.notifyMu.Lock()
	events := a.pendingCodexInteractionsLocked(threadID)
	a.notifyMu.Unlock()
	return events
}

func (a *ACPAgent) rememberPendingCodexInteractionLocked(threadID string, event *codexTurnEvent) {
	requestKey := codexInteractionBrokerKey(event)
	if a.protocol != protocolCodexAppServer || threadID == "" || requestKey == "" {
		return
	}
	a.bindCodexInteractionBrokerLocked(threadID, event)
	if a.pendingTurnInteractions == nil {
		a.pendingTurnInteractions = make(map[string]map[string]*codexTurnEvent)
	}
	if a.pendingTurnInteractions[threadID] == nil {
		a.pendingTurnInteractions[threadID] = make(map[string]*codexTurnEvent)
	}
	a.pendingTurnInteractions[threadID][requestKey] = event
}

func (a *ACPAgent) pendingCodexInteractionsLocked(threadID string) []*codexTurnEvent {
	pending := a.pendingTurnInteractions[strings.TrimSpace(threadID)]
	if len(pending) == 0 {
		return nil
	}
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	events := make([]*codexTurnEvent, 0, len(keys))
	for _, key := range keys {
		events = append(events, pending[key])
	}
	return events
}

func (a *ACPAgent) forgetPendingCodexInteraction(threadID string, event *codexTurnEvent) {
	threadID = strings.TrimSpace(threadID)
	requestKey := codexInteractionBrokerKey(event)
	if threadID == "" || requestKey == "" {
		return
	}
	a.notifyMu.Lock()
	a.forgetCodexInteractionLocked(threadID, requestKey, event)
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
