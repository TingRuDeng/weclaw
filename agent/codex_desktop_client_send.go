package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
	if err := c.writeEnvelope(ctx, options.connection, options.envelope); err != nil {
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
	if err := c.writeEnvelope(ctx, connection, envelope); err != nil {
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
func (c *codexDesktopClient) writeEnvelope(ctx context.Context, connection codexDesktopConnectionRef, envelope codexDesktopEnvelope) error {
	payload, err := encodeCodexDesktopEnvelope(envelope)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.writeMu.Lock()
	if !c.connectionMatches(connection) {
		c.writeMu.Unlock()
		return c.disconnectedError()
	}
	deadline := time.Now().Add(c.writeTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	writeErr := connection.conn.SetWriteDeadline(deadline)
	if writeErr == nil {
		writeErr = writeCodexDesktopFrame(connection.conn, payload)
		if writeErr == nil {
			c.markRequestWritten(envelope.RequestID)
		}
		// A peer may close immediately after receiving and answering the frame.
		// Deadline cleanup is local bookkeeping and must not overwrite a
		// successful delivery; the read loop still classifies any real
		// post-write disconnect through the pending request's written state.
		_ = connection.conn.SetWriteDeadline(time.Time{})
	}
	if writeErr != nil {
		result := c.disconnectEpochLocked(connection)
		c.notifyDisconnectLocked(result, writeErr)
		c.writeMu.Unlock()
		c.failDisconnectedPending(result, writeErr)
		return fmt.Errorf("%w: 写入 method=%s requestId=%s: %v", ErrCodexDesktopDisconnected, envelope.Method, envelope.RequestID, writeErr)
	}
	c.writeMu.Unlock()
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
