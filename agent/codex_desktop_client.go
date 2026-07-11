package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

type codexDesktopClient struct {
	mu      sync.Mutex
	writeMu sync.Mutex

	dial                              func(context.Context) (net.Conn, error)
	conn                              net.Conn
	connectionState                   *codexDesktopConnectionState
	clientID                          string
	epoch, requestSeq                 uint64
	closed, connecting, everConnected bool
	pending                           map[string]*codexDesktopPendingCall
	discovery                         map[string]*codexDesktopPendingDiscovery
	requestID                         func() string
	now                               func() time.Time
	requestTimeout, discoveryTimeout  time.Duration
	onBroadcast                       func(codexDesktopEnvelope)
	broadcastMu                       sync.Mutex
	broadcasts                        []codexDesktopBroadcast
	broadcastWake                     chan struct{}
	broadcastStop, broadcastDone      chan struct{}
	broadcastCloseOnce                sync.Once
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
	client := &codexDesktopClient{
		dial: options.dial, requestID: options.requestID, now: options.now,
		requestTimeout: options.requestTimeout, discoveryTimeout: options.discoveryTimeout,
		onBroadcast: options.onBroadcast, pending: make(map[string]*codexDesktopPendingCall),
		discovery:     make(map[string]*codexDesktopPendingDiscovery),
		broadcastWake: make(chan struct{}, 1),
		broadcastStop: make(chan struct{}), broadcastDone: make(chan struct{}),
	}
	client.startBroadcastWorker()
	return client
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
	conn, epoch, state := c.conn, c.epoch, c.connectionState
	c.mu.Unlock()
	c.stopBroadcastWorker()
	if conn == nil {
		c.waitBroadcastWorker()
		return nil
	}
	err := c.disconnectEpoch(codexDesktopConnectionRef{conn: conn, epoch: epoch, state: state}, ErrCodexDesktopDisconnected)
	c.waitBroadcastWorker()
	return err
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
