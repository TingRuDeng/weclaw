package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// readLoop 独占读取当前连接，并由 epoch 防止旧连接清理新状态。
func (c *codexDesktopClient) readLoop(connection codexDesktopConnectionRef) {
	for {
		payload, err := readCodexDesktopFrame(connection.conn)
		if err != nil {
			_ = c.disconnectEpoch(connection, err)
			return
		}
		envelope, err := decodeCodexDesktopEnvelope(payload)
		if err != nil {
			_ = c.disconnectEpoch(connection, err)
			return
		}
		if err := c.dispatchEnvelope(connection, envelope); err != nil {
			_ = c.disconnectEpoch(connection, err)
			return
		}
	}
}

// dispatchEnvelope 按已校验 envelope 类型分发响应、发现请求和广播。
func (c *codexDesktopClient) dispatchEnvelope(connection codexDesktopConnectionRef, envelope codexDesktopEnvelope) error {
	switch envelope.Type {
	case codexDesktopEnvelopeResponse:
		c.dispatchResponse(envelope)
	case codexDesktopEnvelopeDiscoveryResponse:
		return c.dispatchDiscoveryResponse(envelope)
	case codexDesktopEnvelopeDiscoveryRequest:
		return c.answerDiscovery(connection, envelope.RequestID)
	case codexDesktopEnvelopeBroadcast:
		if c.onBroadcast != nil {
			c.onBroadcast(envelope)
		}
	}
	return nil
}

// dispatchResponse 按 requestId 唤醒唯一调用等待者。
func (c *codexDesktopClient) dispatchResponse(envelope codexDesktopEnvelope) {
	c.mu.Lock()
	pending := c.pending[envelope.RequestID]
	delete(c.pending, envelope.RequestID)
	c.mu.Unlock()
	if pending == nil {
		return
	}
	result := codexDesktopCallResult{result: envelope.Result}
	if envelope.ResultType == codexDesktopResultError {
		result.err = classifyCodexDesktopRemoteError(envelope.Error)
	}
	pending.result <- result
}

// classifyCodexDesktopRemoteError 仅把明确无人处理映射为确定性错误。
func classifyCodexDesktopRemoteError(message string) error {
	if isCodexDesktopNoClientError(message) {
		return fmt.Errorf("%w: %s", ErrCodexDesktopNoClient, message)
	}
	return fmt.Errorf("%w: Codex Desktop 返回错误: %s", ErrCodexDesktopDeliveryUnknown, message)
}

// dispatchDiscoveryResponse 解析 canHandle 并唤醒对应 discovery 等待者。
func (c *codexDesktopClient) dispatchDiscoveryResponse(envelope codexDesktopEnvelope) error {
	var response struct {
		CanHandle bool `json:"canHandle"`
	}
	if err := json.Unmarshal(envelope.Response, &response); err != nil {
		return fmt.Errorf("解析 Codex Desktop discovery response: %w", err)
	}
	c.mu.Lock()
	pending := c.discovery[envelope.RequestID]
	delete(c.discovery, envelope.RequestID)
	c.mu.Unlock()
	if pending != nil {
		pending.result <- codexDesktopDiscoveryResult{canHandle: response.CanHandle}
	}
	return nil
}

// answerDiscovery 在第一阶段始终声明本 client 不能处理 peer 请求。
func (c *codexDesktopClient) answerDiscovery(connection codexDesktopConnectionRef, requestID string) error {
	response, err := json.Marshal(map[string]bool{"canHandle": false})
	if err != nil {
		return err
	}
	envelope := codexDesktopEnvelope{
		Type: codexDesktopEnvelopeDiscoveryResponse, RequestID: requestID, Response: response,
	}
	return c.writeEnvelope(connection, envelope)
}

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
	connection := codexDesktopConnectionRef{conn: c.conn, epoch: c.epoch, connecting: connecting}
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
	c.mu.Lock()
	if c.conn != connection.conn || c.epoch != connection.epoch {
		c.mu.Unlock()
		return nil
	}
	c.conn = nil
	c.clientID = ""
	c.connecting = false
	pending, discovery := c.pending, c.discovery
	c.pending = make(map[string]*codexDesktopPendingCall)
	c.discovery = make(map[string]*codexDesktopPendingDiscovery)
	c.mu.Unlock()

	closeErr := connection.conn.Close()
	err := fmt.Errorf("%w: %w: %v", ErrCodexDesktopDeliveryUnknown, ErrCodexDesktopDisconnected, cause)
	for _, call := range pending {
		call.result <- codexDesktopCallResult{err: err}
	}
	for _, request := range discovery {
		request.result <- codexDesktopDiscoveryResult{err: err}
	}
	return closeErr
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
	c.epoch++
	c.conn = conn
	c.clientID = ""
	return codexDesktopConnectionRef{conn: conn, epoch: c.epoch}, nil
}

// finishInitialize 仅为仍匹配的 epoch 发布 connected 状态。
func (c *codexDesktopClient) finishInitialize(connection codexDesktopConnectionRef, clientID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != connection.conn || c.epoch != connection.epoch {
		return ErrCodexDesktopDisconnected
	}
	c.clientID = clientID
	c.connecting = false
	c.everConnected = true
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

// isCodexDesktopNoClientError 匹配路由器两种已知无人处理错误文本。
func isCodexDesktopNoClientError(message string) bool {
	normalized := strings.ToLower(message)
	return strings.Contains(normalized, "no-client-found") || strings.Contains(normalized, "no codex ipc client can handle")
}
