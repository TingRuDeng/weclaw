package agent

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestCodexDesktopClientBroadcastCallbackCanCall(t *testing.T) {
	callbackResult := make(chan error, 1)
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, Method: "thread-stream-state-changed",
			Version: 11, Params: json.RawMessage(`{"conversationId":"thread-1"}`),
		})
		request := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{
			requestID: request.RequestID,
			value:     map[string]int{"revision": 1},
		})
	})
	options := codexDesktopTestOptions(dial)
	options.requestTimeout = 50 * time.Millisecond
	var client *codexDesktopClient
	options.onBroadcast = func(codexDesktopEnvelope) {
		_, err := client.Call(context.Background(), "thread-follower-load-complete-history", map[string]string{
			"conversationId": "thread-1",
		})
		callbackResult <- err
	}
	client = newCodexDesktopClient(options)
	mustConnectCodexDesktopTestClient(t, client)

	select {
	case err := <-callbackResult:
		if err != nil {
			t.Fatalf("broadcast callback Call() error = %v", err)
		}
	case <-time.After(codexDesktopTestTimeout):
		t.Fatal("broadcast callback 被 IPC read loop 阻塞")
	}
}

func TestCodexDesktopClientBroadcastsKeepArrivalOrder(t *testing.T) {
	methods := make(chan string, 2)
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		serveCodexDesktopTestInitialize(t, conn, "client-1")
		writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, Method: "first", Version: 1,
			Params: json.RawMessage(`{"conversationId":"thread-1"}`),
		})
		writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, Method: "second", Version: 2,
			Params: json.RawMessage(`{"conversationId":"thread-1"}`),
		})
	})
	options := codexDesktopTestOptions(dial)
	options.onBroadcast = func(envelope codexDesktopEnvelope) { methods <- envelope.Method }
	client := newCodexDesktopClient(options)
	mustConnectCodexDesktopTestClient(t, client)

	first := waitCodexDesktopBroadcastMethod(t, methods)
	second := waitCodexDesktopBroadcastMethod(t, methods)
	if first != "first" || second != "second" {
		t.Fatalf("broadcast order = [%s %s]", first, second)
	}
}

func TestCodexDesktopClientDropsBroadcastWhenInitializeFails(t *testing.T) {
	broadcasts := make(chan codexDesktopEnvelope, 1)
	dial := codexDesktopTestDial(t, func(conn net.Conn, _ int) {
		initialize := readCodexDesktopTestEnvelope(t, conn)
		writeCodexDesktopTestEnvelope(t, conn, codexDesktopEnvelope{
			Type: codexDesktopEnvelopeBroadcast, Method: "before-initialize", Version: 1,
			Params: json.RawMessage(`{"conversationId":"thread-1"}`),
		})
		writeCodexDesktopTestSuccess(t, conn, codexDesktopTestResponse{
			requestID: initialize.RequestID, value: map[string]string{},
		})
	})
	options := codexDesktopTestOptions(dial)
	options.onBroadcast = func(envelope codexDesktopEnvelope) { broadcasts <- envelope }
	client := newCodexDesktopClient(options)
	defer client.Close()

	if err := client.Connect(context.Background()); err == nil {
		t.Fatal("Connect() error = nil")
	}
	select {
	case broadcast := <-broadcasts:
		t.Fatalf("unexpected broadcast = %s", broadcast.Method)
	case <-time.After(codexDesktopTestTimeout):
	}
}

func waitCodexDesktopBroadcastMethod(t *testing.T, methods <-chan string) string {
	t.Helper()
	select {
	case method := <-methods:
		return method
	case <-time.After(codexDesktopTestTimeout):
		t.Fatal("broadcast callback not called")
		return ""
	}
}
