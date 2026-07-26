package agent

// startBroadcastWorker 启动有序广播消费者，避免回调阻塞 IPC 读取循环。
func (c *codexDesktopClient) startBroadcastWorker() {
	if c.onBroadcast == nil {
		close(c.broadcastDone)
		return
	}
	go c.runBroadcastWorker()
}

// enqueueBroadcast 只做内存入队，确保读取循环可继续接收回调触发请求的响应。
func (c *codexDesktopClient) enqueueBroadcast(connection codexDesktopConnectionRef, envelope codexDesktopEnvelope) {
	if c.onBroadcast == nil {
		return
	}
	c.broadcastMu.Lock()
	pending := codexDesktopBroadcast{connection: connection, envelope: envelope}
	if len(c.broadcasts) < codexDesktopBroadcastQueueLimit {
		c.broadcasts = append(c.broadcasts, pending)
	} else if index := c.coalescibleBroadcastIndexLocked(pending); index >= 0 {
		c.broadcasts[index] = pending
	} else {
		copy(c.broadcasts, c.broadcasts[1:])
		c.broadcasts[len(c.broadcasts)-1] = pending
	}
	c.broadcastMu.Unlock()
	select {
	case c.broadcastWake <- struct{}{}:
	default:
	}
}

// coalescibleBroadcastIndexLocked 找到同一连接、方法和 thread 的最新待投影项。
// 若中间 patch 被合并，state store 会通过 revision 缺口请求全量快照。
func (c *codexDesktopClient) coalescibleBroadcastIndexLocked(pending codexDesktopBroadcast) int {
	pendingThread := codexDesktopBroadcastThreadID(pending.envelope)
	for index := len(c.broadcasts) - 1; index >= 0; index-- {
		current := c.broadcasts[index]
		if current.connection.epoch == pending.connection.epoch &&
			current.envelope.Method == pending.envelope.Method &&
			codexDesktopBroadcastThreadID(current.envelope) == pendingThread {
			return index
		}
	}
	return -1
}

// runBroadcastWorker 串行调用回调，保持 Desktop 广播到达顺序。
func (c *codexDesktopClient) runBroadcastWorker() {
	defer close(c.broadcastDone)
	for {
		select {
		case <-c.broadcastStop:
			return
		case <-c.broadcastWake:
			if !c.drainBroadcasts() {
				return
			}
		}
	}
}

// drainBroadcasts 逐条等待所属连接完成握手，再串行投递有效广播。
func (c *codexDesktopClient) drainBroadcasts() bool {
	for {
		broadcast, ok := c.nextBroadcast()
		if !ok {
			return true
		}
		if !c.waitBroadcastReady(broadcast.connection) {
			return false
		}
		if broadcast.connection.state != nil &&
			broadcast.connection.state.initialized.Load() &&
			c.connectionMatches(broadcast.connection) {
			c.onBroadcast(broadcast.connection.epoch, broadcast.envelope)
		}
	}
}

// nextBroadcast 从有界内存队列取出最早事件，读取循环不会因投影速度反向阻塞。
func (c *codexDesktopClient) nextBroadcast() (codexDesktopBroadcast, bool) {
	c.broadcastMu.Lock()
	defer c.broadcastMu.Unlock()
	if len(c.broadcasts) == 0 {
		return codexDesktopBroadcast{}, false
	}
	broadcast := c.broadcasts[0]
	c.broadcasts[0] = codexDesktopBroadcast{}
	c.broadcasts = c.broadcasts[1:]
	return broadcast, true
}

// waitBroadcastReady 等待连接握手完成或 client 关闭。
func (c *codexDesktopClient) waitBroadcastReady(connection codexDesktopConnectionRef) bool {
	if connection.state == nil {
		return true
	}
	select {
	case <-connection.state.ready:
		return true
	case <-c.broadcastStop:
		return false
	}
}

// stopBroadcastWorker 只关闭一次广播消费者。
func (c *codexDesktopClient) stopBroadcastWorker() {
	c.broadcastCloseOnce.Do(func() { close(c.broadcastStop) })
}

// waitBroadcastWorker 等待广播消费者结束，避免关闭后仍执行回调。
func (c *codexDesktopClient) waitBroadcastWorker() {
	<-c.broadcastDone
}
