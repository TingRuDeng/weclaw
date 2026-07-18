package agent

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
	defer a.notifyMu.Unlock()
	if a.turnCh[key] != owner {
		return
	}
	delete(a.turnCh, key)
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
