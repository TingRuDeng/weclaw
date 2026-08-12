package agent

// SetCodexThreadActivityHandler 注册 thread 状态变化通知。回调在 Agent
// 内部锁之外执行，调用方应只做非阻塞唤醒。
func (a *ACPAgent) SetCodexThreadActivityHandler(handler func(threadID string)) {
	a.codexThreadActivityMu.Lock()
	a.codexThreadActivityHandler = handler
	a.codexThreadActivityMu.Unlock()
}
