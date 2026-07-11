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
	c.broadcasts = append(c.broadcasts, codexDesktopBroadcast{connection: connection, envelope: envelope})
	c.broadcastMu.Unlock()
	select {
	case c.broadcastWake <- struct{}{}:
	default:
	}
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
		if broadcast.connection.state != nil && broadcast.connection.state.initialized.Load() {
			c.onBroadcast(broadcast.envelope)
		}
	}
}

// nextBroadcast 从无界内存队列取出最早事件，读取循环不会因队列容量反向阻塞。
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
