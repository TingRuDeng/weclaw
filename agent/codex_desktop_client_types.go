package agent

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Desktop Router 最多用 10 秒发现目标 handler，随后才返回 no-client-found。
	// 本地等待必须留出余量，否则会先报交付状态未知，无法进入确定性的远程接管恢复。
	codexDesktopRouterDefaultTimeout = 10 * time.Second
	codexDesktopRequestTimeout       = codexDesktopRouterDefaultTimeout + 2*time.Second
	codexDesktopDiscoveryTimeout     = 1500 * time.Millisecond
	codexDesktopInitialClientID      = "initializing-client"
)

type codexDesktopCallResult struct {
	result json.RawMessage
	err    error
}

type codexDesktopPendingCall struct {
	result  chan codexDesktopCallResult
	written bool
}

type codexDesktopDiscoveryResult struct {
	canHandle bool
	err       error
}

type codexDesktopPendingDiscovery struct {
	result  chan codexDesktopDiscoveryResult
	written bool
}

type codexDesktopClientOptions struct {
	dial                             func(context.Context) (net.Conn, error)
	requestID                        func() string
	now                              func() time.Time
	requestTimeout, discoveryTimeout time.Duration
	onBroadcast                      func(codexDesktopEnvelope)
	// onDisconnect 在 client 的写串行区内调用，不得反向调用 client 方法。
	onDisconnect func(error)
}

type codexDesktopConnectionState struct {
	ready       chan struct{}
	readyOnce   sync.Once
	initialized atomic.Bool
}

type codexDesktopConnectionRef struct {
	conn       net.Conn
	epoch      uint64
	connecting bool
	state      *codexDesktopConnectionState
}

func (r codexDesktopConnectionRef) markReady(initialized bool) {
	if r.state == nil {
		return
	}
	if initialized {
		r.state.initialized.Store(true)
	}
	r.state.readyOnce.Do(func() { close(r.state.ready) })
}

type codexDesktopCallOptions struct {
	envelope   codexDesktopEnvelope
	timeout    time.Duration
	connection codexDesktopConnectionRef
}

type codexDesktopBroadcast struct {
	connection codexDesktopConnectionRef
	envelope   codexDesktopEnvelope
}
