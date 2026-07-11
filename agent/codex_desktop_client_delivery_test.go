package agent

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

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
