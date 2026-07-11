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
	codexDesktopRequestTimeout   = 10 * time.Second
	codexDesktopDiscoveryTimeout = 1500 * time.Millisecond
	codexDesktopInitialClientID  = "initializing-client"
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
