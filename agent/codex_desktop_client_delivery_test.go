package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexDesktopClientClassifiesIncompleteFrameAsDisconnected(t *testing.T) {
	tests := []struct {
		name string
		mode codexDesktopFailingWriteMode
	}{
		{"zero byte write", codexDesktopFailingWriteZero},
		{"partial header", codexDesktopFailingWriteHeader},
		{"partial payload", codexDesktopFailingWritePayload},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, failing := newCodexDesktopFailingWriteClient(t)
			mustConnectCodexDesktopTestClient(t, client)
			failing.arm(test.mode)

			_, err := client.Call(context.Background(), "thread-follower-start-turn", map[string]string{"prompt": "secret"})
			if !errors.Is(err, ErrCodexDesktopDisconnected) || errors.Is(err, ErrCodexDesktopDeliveryUnknown) {
				t.Fatalf("Call() error = %v, want disconnected only", err)
			}
			assertCodexDesktopWriteFailureCleanup(t, client, failing)
		})
	}
}

func TestCodexDesktopClientReturnsDeliveryUnknownAfterWrite(t *testing.T) {
	written := make(chan struct{})
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		_ = readCodexDesktopTestEnvelope(t, conn)
		close(written)
		<-time.After(codexDesktopTestTimeout)
	})
	options := codexDesktopTestOptions(dial)
	options.requestTimeout = 20 * time.Millisecond
	client := newCodexDesktopClient(options)
	mustConnectCodexDesktopTestClient(t, client)

	_, err := client.Call(context.Background(), "thread-follower-start-turn", map[string]string{"prompt": "secret"})
	<-written
	if !errors.Is(err, ErrCodexDesktopDeliveryUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want delivery unknown and deadline", err)
	}
}

func TestCodexDesktopClientDoesNotRetryAmbiguousStartTurn(t *testing.T) {
	var requests atomic.Int32
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		_ = readCodexDesktopTestEnvelope(t, conn)
		requests.Add(1)
		<-time.After(codexDesktopTestTimeout)
	})
	options := codexDesktopTestOptions(dial)
	options.requestTimeout = 20 * time.Millisecond
	client := newCodexDesktopClient(options)
	mustConnectCodexDesktopTestClient(t, client)

	_, err := client.Call(context.Background(), "thread-follower-start-turn", map[string]string{"prompt": "secret"})
	if !errors.Is(err, ErrCodexDesktopDeliveryUnknown) || requests.Load() != 1 {
		t.Fatalf("Call() error = %v, requests = %d", err, requests.Load())
	}
}

func TestCodexDesktopClientAnswersDiscoveryFalse(t *testing.T) {
	response := make(chan codexDesktopEnvelope, 1)
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		discovery, err := newCodexDesktopDiscoveryRequest(codexDesktopDiscoverySpec{
			RequestID: "peer-discovery", SourceClientID: "peer", Method: "thread-follower-start-turn", Params: map[string]string{"id": "turn-1"},
		})
		if err != nil {
			t.Errorf("new discovery: %v", err)
			return
		}
		writeCodexDesktopTestEnvelope(t, conn, discovery)
		response <- readCodexDesktopTestEnvelope(t, conn)
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))
	mustConnectCodexDesktopTestClient(t, client)

	got := <-response
	if got.Type != codexDesktopEnvelopeDiscoveryResponse || got.RequestID != "peer-discovery" || string(got.Response) != `{"canHandle":false}` {
		t.Fatalf("discovery response = %+v", got)
	}
}

func TestCodexDesktopClientMapsNoClientFound(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		request := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestError(t, conn, codexDesktopTestResponse{requestID: request.RequestID, message: "no-client-found"})
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))
	mustConnectCodexDesktopTestClient(t, client)

	_, err := client.Call(context.Background(), "thread-follower-start-turn", map[string]string{"id": "turn-1"})
	if !errors.Is(err, ErrCodexDesktopNoClient) || errors.Is(err, ErrCodexDesktopDeliveryUnknown) {
		t.Fatalf("Call() error = %v, want no client only", err)
	}
}

func TestCodexDesktopClientDiscoveryReturnsBoolean(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		request := readCodexDesktopTestEnvelope(t, conn)
		if request.Type != codexDesktopEnvelopeDiscoveryRequest {
			t.Errorf("type = %s", request.Type)
		}
		writeCodexDesktopTestDiscovery(t, conn, codexDesktopTestResponse{requestID: request.RequestID, canHandle: true})
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))
	mustConnectCodexDesktopTestClient(t, client)

	canHandle, err := client.Discover(context.Background(), codexDesktopRequestSpec{Method: "thread-follower-interrupt-turn", Params: map[string]string{"id": "turn-1"}})
	if err != nil || !canHandle {
		t.Fatalf("Discover() = %v, %v", canHandle, err)
	}
}

func TestCodexDesktopClientDiscoveryTimeoutIsAmbiguous(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		_ = readCodexDesktopTestEnvelope(t, conn)
		<-time.After(codexDesktopTestTimeout)
	})
	options := codexDesktopTestOptions(dial)
	options.discoveryTimeout = 20 * time.Millisecond
	client := newCodexDesktopClient(options)
	mustConnectCodexDesktopTestClient(t, client)

	_, err := client.Discover(context.Background(), codexDesktopRequestSpec{Method: "thread-follower-start-turn", Params: map[string]string{"id": "turn-1"}})
	if !errors.Is(err, ErrCodexDesktopDeliveryUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Discover() error = %v", err)
	}
}

type codexDesktopFailingWriteMode int32

const (
	codexDesktopFailingWriteOff codexDesktopFailingWriteMode = iota
	codexDesktopFailingWriteZero
	codexDesktopFailingWriteHeader
	codexDesktopFailingWritePayload
)

type codexDesktopFailingConn struct {
	net.Conn
	mode   atomic.Int32
	writes atomic.Int32
	closed atomic.Bool
}

func (c *codexDesktopFailingConn) arm(mode codexDesktopFailingWriteMode) {
	c.writes.Store(0)
	c.mode.Store(int32(mode))
}

func (c *codexDesktopFailingConn) Write(payload []byte) (int, error) {
	mode := codexDesktopFailingWriteMode(c.mode.Load())
	if mode == codexDesktopFailingWriteOff {
		return c.Conn.Write(payload)
	}
	write := c.writes.Add(1)
	if mode == codexDesktopFailingWriteZero || write > 2 {
		return 0, nil
	}
	if mode == codexDesktopFailingWriteHeader {
		return c.writePartial(payload, write == 1, 2)
	}
	if write == 1 {
		return c.Conn.Write(payload)
	}
	return c.writePartial(payload, true, 3)
}

func (c *codexDesktopFailingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func (c *codexDesktopFailingConn) writePartial(payload []byte, partial bool, limit int) (int, error) {
	if !partial {
		return 0, nil
	}
	if len(payload) < limit {
		limit = len(payload)
	}
	return c.Conn.Write(payload[:limit])
}

func newCodexDesktopFailingWriteClient(t *testing.T) (*codexDesktopClient, *codexDesktopFailingConn) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	failing := &codexDesktopFailingConn{Conn: clientConn}
	go func() {
		defer serverConn.Close()
		serveCodexDesktopTestInitialize(t, serverConn, "client-1")
		_, _ = readCodexDesktopFrame(serverConn)
	}()
	options := codexDesktopTestOptions(func(context.Context) (net.Conn, error) { return failing, nil })
	return newCodexDesktopClient(options), failing
}

func assertCodexDesktopWriteFailureCleanup(t *testing.T, client *codexDesktopClient, conn *codexDesktopFailingConn) {
	t.Helper()
	client.mu.Lock()
	pending, discovery := len(client.pending), len(client.discovery)
	connection := client.conn
	client.mu.Unlock()
	if !conn.closed.Load() || connection != nil || pending != 0 || discovery != 0 || client.IsConnected() {
		t.Fatalf("cleanup closed=%v conn=%v pending=%d discovery=%d connected=%v", conn.closed.Load(), connection, pending, discovery, client.IsConnected())
	}
}

var _ io.Writer = (*codexDesktopFailingConn)(nil)
