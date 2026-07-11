package agent

import (
	"encoding/json"
	"fmt"
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
		c.enqueueBroadcast(connection, envelope)
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
