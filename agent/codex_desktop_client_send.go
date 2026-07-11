package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// initialize 完成固定 clientType 的首帧握手并提取 clientId。
func (c *codexDesktopClient) initialize(ctx context.Context, connection codexDesktopConnectionRef) (string, error) {
	envelope, err := newCodexDesktopRequest(codexDesktopRequestSpec{
		RequestID: c.nextRequestID(), SourceClientID: codexDesktopInitialClientID,
		Method: "initialize", Params: map[string]string{"clientType": "weclaw"},
	})
	if err != nil {
		return "", err
	}
	connection.connecting = true
	result, err := c.sendCallOnConnection(ctx, codexDesktopCallOptions{
		envelope: envelope, timeout: c.requestTimeout, connection: connection,
	})
	if err != nil {
		return "", err
	}
	var response struct {
		ClientID string `json:"clientId"`
	}
	if len(result) == 0 || result[0] != '{' || json.Unmarshal(result, &response) != nil || strings.TrimSpace(response.ClientID) == "" {
		return "", fmt.Errorf("Codex Desktop initialize result 缺少非空 clientId")
	}
	return response.ClientID, nil
}

func (c *codexDesktopClient) sendCall(ctx context.Context, options codexDesktopCallOptions) (json.RawMessage, error) {
	connection, err := c.connectionForWrite(false)
	if err != nil {
		return nil, err
	}
	options.connection = connection
	return c.sendCallOnConnection(ctx, options)
}

func (c *codexDesktopClient) sendCallOnConnection(ctx context.Context, options codexDesktopCallOptions) (json.RawMessage, error) {
	pending := &codexDesktopPendingCall{result: make(chan codexDesktopCallResult, 1)}
	requestID := options.envelope.RequestID
	if err := c.registerCall(requestID, pending, options.connection); err != nil {
		return nil, err
	}
	if err := c.writeEnvelope(options.connection, options.envelope); err != nil {
		c.removeCall(requestID, pending)
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	select {
	case result := <-pending.result:
		return result.result, result.err
	case <-waitCtx.Done():
		c.removeCall(requestID, pending)
		return nil, fmt.Errorf("%w: %w", ErrCodexDesktopDeliveryUnknown, waitCtx.Err())
	}
}

func (c *codexDesktopClient) sendDiscovery(ctx context.Context, envelope codexDesktopEnvelope) (bool, error) {
	connection, err := c.connectionForWrite(false)
	if err != nil {
		return false, err
	}
	pending := &codexDesktopPendingDiscovery{result: make(chan codexDesktopDiscoveryResult, 1)}
	if err := c.registerDiscovery(envelope.RequestID, pending, connection); err != nil {
		return false, err
	}
	if err := c.writeEnvelope(connection, envelope); err != nil {
		c.removeDiscovery(envelope.RequestID, pending)
		return false, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, c.discoveryTimeout)
	defer cancel()
	select {
	case result := <-pending.result:
		return result.canHandle, result.err
	case <-waitCtx.Done():
		c.removeDiscovery(envelope.RequestID, pending)
		return false, fmt.Errorf("%w: %w", ErrCodexDesktopDeliveryUnknown, waitCtx.Err())
	}
}

// writeEnvelope 串行化整帧写入，并在写入失败时终止对应 epoch。
func (c *codexDesktopClient) writeEnvelope(connection codexDesktopConnectionRef, envelope codexDesktopEnvelope) error {
	payload, err := encodeCodexDesktopEnvelope(envelope)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if !c.connectionMatches(connection) {
		return c.disconnectedError()
	}
	if err := writeCodexDesktopFrame(connection.conn, payload); err != nil {
		_ = c.disconnectEpochLocked(connection, err)
		return fmt.Errorf("%w: 写入 method=%s requestId=%s: %v", ErrCodexDesktopDisconnected, envelope.Method, envelope.RequestID, err)
	}
	c.markRequestWritten(envelope.RequestID)
	return nil
}

func (c *codexDesktopClient) markRequestWritten(requestID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pending := c.pending[requestID]; pending != nil {
		pending.written = true
	}
	if pending := c.discovery[requestID]; pending != nil {
		pending.written = true
	}
}

func isCodexDesktopNoClientError(message string) bool {
	normalized := strings.ToLower(message)
	return strings.Contains(normalized, "no-client-found") || strings.Contains(normalized, "no codex ipc client can handle")
}
