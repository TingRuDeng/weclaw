package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	codexDesktopRequestTimeout   = 10 * time.Second
	codexDesktopDiscoveryTimeout = 1500 * time.Millisecond
	codexDesktopInitialClientID  = "initializing-client"
)

type codexDesktopCallResult struct {
	result json.RawMessage
	err    error
}

type codexDesktopPendingCall struct {
	result chan codexDesktopCallResult
}

type codexDesktopDiscoveryResult struct {
	canHandle bool
	err       error
}

type codexDesktopPendingDiscovery struct {
	result chan codexDesktopDiscoveryResult
}

type codexDesktopClientOptions struct {
	dial             func(context.Context) (net.Conn, error)
	requestID        func() string
	now              func() time.Time
	requestTimeout   time.Duration
	discoveryTimeout time.Duration
	onBroadcast      func(codexDesktopEnvelope)
}

type codexDesktopConnectionRef struct {
	conn       net.Conn
	epoch      uint64
	connecting bool
}

type codexDesktopCallOptions struct {
	envelope   codexDesktopEnvelope
	timeout    time.Duration
	connection codexDesktopConnectionRef
}

type codexDesktopClient struct {
	mu      sync.Mutex
	writeMu sync.Mutex

	dial             func(context.Context) (net.Conn, error)
	conn             net.Conn
	clientID         string
	epoch            uint64
	closed           bool
	connecting       bool
	everConnected    bool
	pending          map[string]*codexDesktopPendingCall
	discovery        map[string]*codexDesktopPendingDiscovery
	requestID        func() string
	now              func() time.Time
	requestSeq       uint64
	requestTimeout   time.Duration
	discoveryTimeout time.Duration
	onBroadcast      func(codexDesktopEnvelope)
}

// newCodexDesktopClient 创建可注入传输、时钟和超时的 IPC client。
func newCodexDesktopClient(options codexDesktopClientOptions) *codexDesktopClient {
	if options.dial == nil {
		options.dial = dialCodexDesktopEndpoint
	}
	if options.now == nil {
		options.now = time.Now
	}
	if options.requestTimeout <= 0 {
		options.requestTimeout = codexDesktopRequestTimeout
	}
	if options.discoveryTimeout <= 0 {
		options.discoveryTimeout = codexDesktopDiscoveryTimeout
	}
	return &codexDesktopClient{
		dial: options.dial, requestID: options.requestID, now: options.now,
		requestTimeout: options.requestTimeout, discoveryTimeout: options.discoveryTimeout,
		onBroadcast: options.onBroadcast, pending: make(map[string]*codexDesktopPendingCall),
		discovery: make(map[string]*codexDesktopPendingDiscovery),
	}
}

// Connect 建立安全连接，并在暴露 connected 状态前完成 initialize 握手。
func (c *codexDesktopClient) Connect(ctx context.Context) error {
	shouldConnect, err := c.beginConnect()
	if err != nil {
		return err
	}
	if !shouldConnect {
		return nil
	}
	conn, err := c.dial(ctx)
	if err != nil {
		c.finishDialFailure()
		return fmt.Errorf("%w: %v", ErrCodexDesktopUnavailable, err)
	}
	connection, err := c.installConnection(conn)
	if err != nil {
		_ = conn.Close()
		return err
	}
	go c.readLoop(connection)
	clientID, err := c.initialize(ctx, connection)
	if err != nil {
		_ = c.disconnectEpoch(connection, err)
		return err
	}
	return c.finishInitialize(connection, clientID)
}

// Call 发送一次不自动重试的 Desktop 请求。
func (c *codexDesktopClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	requestID := c.nextRequestID()
	sourceClientID, err := c.connectedClientID()
	if err != nil {
		return nil, err
	}
	envelope, err := newCodexDesktopRequest(codexDesktopRequestSpec{
		RequestID: requestID, SourceClientID: sourceClientID, Method: method, Params: params,
	})
	if err != nil {
		return nil, err
	}
	return c.sendCall(ctx, codexDesktopCallOptions{envelope: envelope, timeout: c.requestTimeout})
}

// Discover 查询当前是否存在能处理嵌套请求的 Desktop client。
func (c *codexDesktopClient) Discover(ctx context.Context, spec codexDesktopRequestSpec) (bool, error) {
	spec.RequestID = c.nextRequestID()
	sourceClientID, err := c.connectedClientID()
	if err != nil {
		return false, err
	}
	spec.SourceClientID = sourceClientID
	envelope, err := newCodexDesktopDiscoveryRequest(codexDesktopDiscoverySpec(spec))
	if err != nil {
		return false, err
	}
	return c.sendDiscovery(ctx, envelope)
}

// Close 永久关闭 client，并一次性唤醒所有等待者。
func (c *codexDesktopClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn, epoch := c.conn, c.epoch
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return c.disconnectEpoch(codexDesktopConnectionRef{conn: conn, epoch: epoch}, ErrCodexDesktopDisconnected)
}

// IsConnected 返回 initialize 已成功且连接仍属于当前 epoch 的状态。
func (c *codexDesktopClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnectedLocked()
}

// Epoch 返回当前连接代次，供重连状态检查使用。
func (c *codexDesktopClient) Epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

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

// sendCall 在已连接状态下锁定当前连接代次。
func (c *codexDesktopClient) sendCall(ctx context.Context, options codexDesktopCallOptions) (json.RawMessage, error) {
	connection, err := c.connectionForWrite(false)
	if err != nil {
		return nil, err
	}
	options.connection = connection
	return c.sendCallOnConnection(ctx, options)
}

// sendCallOnConnection 注册响应等待者，并在完整写入后应用交付不确定语义。
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

// sendDiscovery 发送 discovery 帧并等待对应布尔响应。
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
		_ = c.disconnectEpoch(connection, err)
		return fmt.Errorf("%w: 写入 method=%s requestId=%s: %v", ErrCodexDesktopDeliveryUnknown, envelope.Method, envelope.RequestID, err)
	}
	return nil
}
