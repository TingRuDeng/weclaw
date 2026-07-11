package agent

import (
	"fmt"
	"net"
)

// registerCall 在写入前登记唯一请求等待者。
func (c *codexDesktopClient) registerCall(requestID string, pending *codexDesktopPendingCall, connection codexDesktopConnectionRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connectionMatchesLocked(connection) {
		return c.stateErrorLocked()
	}
	if _, exists := c.pending[requestID]; exists {
		return fmt.Errorf("Codex Desktop requestId %s 重复", requestID)
	}
	c.pending[requestID] = pending
	return nil
}

// registerDiscovery 在写入前登记唯一 discovery 等待者。
func (c *codexDesktopClient) registerDiscovery(requestID string, pending *codexDesktopPendingDiscovery, connection codexDesktopConnectionRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connectionMatchesLocked(connection) {
		return c.stateErrorLocked()
	}
	if _, exists := c.discovery[requestID]; exists {
		return fmt.Errorf("Codex Desktop discovery requestId %s 重复", requestID)
	}
	c.discovery[requestID] = pending
	return nil
}

// removeCall 仅删除仍指向同一等待者的请求，避免误删复用 ID。
func (c *codexDesktopClient) removeCall(requestID string, pending *codexDesktopPendingCall) {
	c.mu.Lock()
	if c.pending[requestID] == pending {
		delete(c.pending, requestID)
	}
	c.mu.Unlock()
}

// removeDiscovery 仅删除仍指向同一等待者的 discovery 请求。
func (c *codexDesktopClient) removeDiscovery(requestID string, pending *codexDesktopPendingDiscovery) {
	c.mu.Lock()
	if c.discovery[requestID] == pending {
		delete(c.discovery, requestID)
	}
	c.mu.Unlock()
}

// connectionForWrite 返回当前状态允许写入的不可变连接快照。
func (c *codexDesktopClient) connectionForWrite(connecting bool) (codexDesktopConnectionRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	connection := codexDesktopConnectionRef{
		conn: c.conn, epoch: c.epoch, connecting: connecting, state: c.connectionState,
	}
	if !c.connectionMatchesLocked(connection) {
		return codexDesktopConnectionRef{}, c.stateErrorLocked()
	}
	return connection, nil
}

// connectionMatches 在锁内核对连接代次与连接阶段。
func (c *codexDesktopClient) connectionMatches(connection codexDesktopConnectionRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectionMatchesLocked(connection)
}

// connectionMatchesLocked 在持锁状态下检查连接快照是否仍有效。
func (c *codexDesktopClient) connectionMatchesLocked(connection codexDesktopConnectionRef) bool {
	if c.closed || c.conn == nil || c.conn != connection.conn || c.epoch != connection.epoch {
		return false
	}
	if connection.connecting {
		return c.connecting
	}
	return c.isConnectedLocked()
}

// disconnectEpoch 仅清理匹配代次，并一次性失败全部在途请求。
func (c *codexDesktopClient) disconnectEpoch(connection codexDesktopConnectionRef, cause error) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.disconnectEpochLocked(connection, cause)
}

// disconnectEpochLocked 在写锁内按完整写入状态分类所有等待者。
func (c *codexDesktopClient) disconnectEpochLocked(connection codexDesktopConnectionRef, cause error) error {
	c.mu.Lock()
	if c.conn != connection.conn || c.epoch != connection.epoch {
		c.mu.Unlock()
		return nil
	}
	state := c.connectionState
	c.conn, c.connectionState, c.clientID, c.connecting = nil, nil, "", false
	pending, discovery := c.pending, c.discovery
	c.pending = make(map[string]*codexDesktopPendingCall)
	c.discovery = make(map[string]*codexDesktopPendingDiscovery)
	c.mu.Unlock()

	stateRef := codexDesktopConnectionRef{state: state}
	stateRef.markReady(false)
	closeErr := connection.conn.Close()
	c.failPending(pending, discovery, cause)
	return closeErr
}

// failPending 唤醒当前连接代次的全部调用和发现请求。
func (c *codexDesktopClient) failPending(pending map[string]*codexDesktopPendingCall, discovery map[string]*codexDesktopPendingDiscovery, cause error) {
	for _, call := range pending {
		call.result <- codexDesktopCallResult{err: codexDesktopPendingDisconnectError(call.written, cause)}
	}
	for _, request := range discovery {
		request.result <- codexDesktopDiscoveryResult{err: codexDesktopPendingDisconnectError(request.written, cause)}
	}
}

// codexDesktopPendingDisconnectError 仅将完整写入的请求标为交付状态未知。
func codexDesktopPendingDisconnectError(written bool, cause error) error {
	if written {
		return fmt.Errorf("%w: %w: %v", ErrCodexDesktopDeliveryUnknown, ErrCodexDesktopDisconnected, cause)
	}
	return fmt.Errorf("%w: %v", ErrCodexDesktopDisconnected, cause)
}

// beginConnect 串行化连接尝试，并允许已连接调用幂等返回。
func (c *codexDesktopClient) beginConnect() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false, ErrCodexDesktopDisconnected
	}
	if c.isConnectedLocked() {
		return false, nil
	}
	if c.connecting {
		return false, fmt.Errorf("%w: 正在连接", ErrCodexDesktopUnavailable)
	}
	c.connecting = true
	return true, nil
}

// finishDialFailure 清除尚未安装连接的 connecting 状态。
func (c *codexDesktopClient) finishDialFailure() {
	c.mu.Lock()
	c.connecting = false
	c.mu.Unlock()
}

// installConnection 安装新连接并递增 epoch。
func (c *codexDesktopClient) installConnection(conn net.Conn) (codexDesktopConnectionRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		c.connecting = false
		return codexDesktopConnectionRef{}, ErrCodexDesktopDisconnected
	}
	state := &codexDesktopConnectionState{ready: make(chan struct{})}
	c.epoch, c.conn, c.connectionState, c.clientID = c.epoch+1, conn, state, ""
	return codexDesktopConnectionRef{conn: conn, epoch: c.epoch, state: state}, nil
}

// finishInitialize 先发布 connected 状态，再放行握手期间到达的广播。
func (c *codexDesktopClient) finishInitialize(connection codexDesktopConnectionRef, clientID string) error {
	c.mu.Lock()
	if c.conn != connection.conn || c.epoch != connection.epoch {
		c.mu.Unlock()
		connection.markReady(false)
		return ErrCodexDesktopDisconnected
	}
	c.clientID, c.connecting, c.everConnected = clientID, false, true
	c.mu.Unlock()
	connection.markReady(true)
	return nil
}

// nextRequestID 使用注入生成器，或由时钟与单调序号生成稳定唯一 ID。
func (c *codexDesktopClient) nextRequestID() string {
	if c.requestID != nil {
		return c.requestID()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestSeq++
	return fmt.Sprintf("weclaw-%d-%d", c.now().UnixNano(), c.requestSeq)
}

// disconnectedError 返回当前连接历史对应的未连接分类。
func (c *codexDesktopClient) disconnectedError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateErrorLocked()
}

// stateErrorLocked 区分从未连接与已发生断线。
func (c *codexDesktopClient) stateErrorLocked() error {
	if c.everConnected {
		return ErrCodexDesktopDisconnected
	}
	return ErrCodexDesktopUnavailable
}

// isConnectedLocked 要求握手完成、连接存在且 client 未关闭。
func (c *codexDesktopClient) isConnectedLocked() bool {
	return !c.closed && !c.connecting && c.conn != nil && c.clientID != ""
}

// connectedClientID 只在 initialize 已发布成功后返回路由身份。
func (c *codexDesktopClient) connectedClientID() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isConnectedLocked() {
		return "", c.stateErrorLocked()
	}
	return c.clientID, nil
}
