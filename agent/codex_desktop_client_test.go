package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const codexDesktopTestTimeout = 200 * time.Millisecond

func TestCodexDesktopClientInitializesBeforeRequests(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		initialize := readCodexDesktopTestEnvelope(t, conn)
		if initialize.Method != "initialize" || initialize.SourceClientID != "initializing-client" {
			t.Errorf("initialize = %s from %s", initialize.Method, initialize.SourceClientID)
		}
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: initialize.RequestID, value: map[string]string{"clientId": "client-1"}})
		request := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: request.RequestID, value: map[string]bool{"ok": true}})
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))

	if client.IsConnected() {
		t.Fatal("client initialized before Connect")
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	result, err := client.Call(context.Background(), "thread-follower-load-complete-history", map[string]string{"conversationId": "thread-1"})
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("Call() = %s, %v", result, err)
	}
}

func TestCodexDesktopClientCorrelatesConcurrentResponses(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		first := readCodexDesktopTestEnvelope(t, conn)
		second := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: second.RequestID, value: second.Method})
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: first.RequestID, value: first.Method})
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))
	mustConnectCodexDesktopTestClient(t, client)

	methods := []string{"thread-follower-steer-turn", "thread-follower-interrupt-turn"}
	results := make(chan string, len(methods))
	for _, method := range methods {
		go func(method string) {
			result, err := client.Call(context.Background(), method, map[string]string{"id": method})
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			var got string
			_ = json.Unmarshal(result, &got)
			results <- got
		}(method)
	}
	seen := map[string]bool{<-results: true, <-results: true}
	for _, method := range methods {
		if !seen[method] {
			t.Fatalf("results = %v, missing %s", seen, method)
		}
	}
}

func TestCodexDesktopClientFailsPendingCallsOnDisconnect(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		_ = readCodexDesktopTestEnvelope(t, conn)
		_ = conn.Close()
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))
	mustConnectCodexDesktopTestClient(t, client)

	_, err := client.Call(context.Background(), "thread-follower-start-turn", map[string]string{"prompt": "secret"})
	if !errors.Is(err, ErrCodexDesktopDeliveryUnknown) {
		t.Fatalf("Call() error = %v, want delivery unknown", err)
	}
}

func TestCodexDesktopClientRejectsMalformedInitialize(t *testing.T) {
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		initialize := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: initialize.RequestID, value: map[string]string{}})
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))

	err := client.Connect(context.Background())
	if err == nil || client.IsConnected() {
		t.Fatalf("Connect() error = %v, connected = %v", err, client.IsConnected())
	}
}

func TestCodexDesktopClientReconnectKeepsNewEpoch(t *testing.T) {
	secondReady := make(chan struct{})
	closeFirst := make(chan struct{})
	dial := codexDesktopTestDial(t, func(conn net.Conn, session int) {
		serveCodexDesktopTestInitialize(t, conn, fmt.Sprintf("client-%d", session))
		if session == 1 {
			<-closeFirst
			_ = conn.Close()
			return
		}
		close(secondReady)
		request := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: request.RequestID, value: map[string]bool{"ok": true}})
	})
	client := newCodexDesktopClient(codexDesktopTestOptions(dial))
	mustConnectCodexDesktopTestClient(t, client)
	close(closeFirst)
	waitCodexDesktopDisconnected(t, client)
	mustConnectCodexDesktopTestClient(t, client)
	<-secondReady

	if client.Epoch() != 2 || !client.IsConnected() {
		t.Fatalf("epoch = %d, connected = %v", client.Epoch(), client.IsConnected())
	}
	if _, err := client.Call(context.Background(), "thread-follower-load-complete-history", map[string]string{"id": "thread-1"}); err != nil {
		t.Fatalf("Call() after reconnect error = %v", err)
	}
}

func TestCodexDesktopClientDispatchesValidatedBroadcastVersion(t *testing.T) {
	broadcasts := make(chan codexDesktopEnvelope, 1)
	release := make(chan struct{})
	defer close(release)
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, Method: "thread-stream-state-changed", Version: 11, Params: json.RawMessage(`{"threadId":"thread-1"}`),
		})
		<-release
	})
	options := codexDesktopTestOptions(dial)
	options.onBroadcast = func(envelope codexDesktopEnvelope) { broadcasts <- envelope }
	client := newCodexDesktopClient(options)
	mustConnectCodexDesktopTestClient(t, client)

	select {
	case got := <-broadcasts:
		if got.Version != 11 {
			t.Fatalf("broadcast version = %d", got.Version)
		}
	case <-time.After(codexDesktopTestTimeout):
		t.Fatal("broadcast callback not called")
	}
}

func codexDesktopTestOptions(dial func(context.Context) (net.Conn, error)) codexDesktopClientOptions {
	var next atomic.Int32
	return codexDesktopClientOptions{
		dial:             dial,
		requestID:        func() string { return fmt.Sprintf("request-%d", next.Add(1)) },
		now:              time.Now,
		requestTimeout:   codexDesktopTestTimeout,
		discoveryTimeout: codexDesktopTestTimeout,
	}
}

func codexDesktopTestDial(t *testing.T, serve func(net.Conn, int)) func(context.Context) (net.Conn, error) {
	t.Helper()
	var mu sync.Mutex
	session := 0
	return func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		mu.Lock()
		session++
		current := session
		mu.Unlock()
		go func() {
			defer server.Close()
			serve(server, current)
		}()
		return client, nil
	}
}

func mustConnectCodexDesktopTestClient(t *testing.T, client *codexDesktopClient) {
	t.Helper()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
}

func waitCodexDesktopDisconnected(t *testing.T, client *codexDesktopClient) {
	t.Helper()
	deadline := time.Now().Add(codexDesktopTestTimeout)
	for client.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if client.IsConnected() {
		t.Fatal("client did not observe disconnect")
	}
}

func serveCodexDesktopTestInitialize(t *testing.T, conn net.Conn, clientID string) {
	t.Helper()
	request := readCodexDesktopTestEnvelope(t, conn)
	writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{requestID: request.RequestID, value: map[string]string{"clientId": clientID}})
}

func readCodexDesktopTestEnvelope(t *testing.T, conn net.Conn) codexDesktopEnvelope {
	t.Helper()
	payload, err := readCodexDesktopFrame(conn)
	if err != nil {
		t.Errorf("read frame: %v", err)
		return codexDesktopEnvelope{}
	}
	envelope, err := decodeCodexDesktopEnvelope(payload)
	if err != nil {
		t.Errorf("decode envelope: %v", err)
	}
	return envelope
}

type codexDesktopTestResponse struct {
	requestID string
	value     any
	message   string
	canHandle bool
}

func writeCodexDesktopTestSuccess(t *testing.T, conn net.Conn, response codexDesktopTestResponse) {
	t.Helper()
	payload, err := json.Marshal(response.value)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{Type: codexDesktopEnvelopeResponse, RequestID: response.requestID, ResultType: codexDesktopResultSuccess, Result: payload})
}

func writeCodexDesktopTestError(t *testing.T, conn net.Conn, response codexDesktopTestResponse) {
	t.Helper()
	writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{Type: codexDesktopEnvelopeResponse, RequestID: response.requestID, ResultType: codexDesktopResultError, Error: response.message})
}

func writeCodexDesktopTestDiscovery(t *testing.T, conn net.Conn, response codexDesktopTestResponse) {
	t.Helper()
	payload, _ := json.Marshal(map[string]bool{"canHandle": response.canHandle})
	writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{Type: codexDesktopEnvelopeDiscoveryResponse, RequestID: response.requestID, Response: payload})
}

func writeCodexDesktopTestEnvelope(t *testing.T, conn net.Conn, envelope codexDesktopEnvelope) {
	t.Helper()
	payload, err := encodeCodexDesktopEnvelope(envelope)
	if err != nil {
		t.Errorf("encode envelope: %v", err)
		return
	}
	if err := writeCodexDesktopFrame(conn, payload); err != nil {
		t.Errorf("write frame: %v", err)
	}
}
