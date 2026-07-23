package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalRPCRequest(t *testing.T) {
	data, err := marshalRPCRequest(7, "session/prompt", map[string]string{"sessionId": "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	var request rpcRequest
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.JSONRPC != "2.0" || request.ID != 7 || request.Method != "session/prompt" {
		t.Fatalf("request=%+v", request)
	}
	if params, ok := request.Params.(map[string]interface{}); !ok || params["sessionId"] != "session-1" {
		t.Fatalf("params=%#v", request.Params)
	}
}

func TestMarshalRPCNotificationOmitsIDAndEmptyParams(t *testing.T) {
	data, err := marshalRPCNotification("session/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `"id"`) || strings.Contains(text, `"params"`) {
		t.Fatalf("notification=%s, want no id or empty params", text)
	}
}

func TestUnmarshalRPCMessageClassifiesResponseAndNotification(t *testing.T) {
	tests := []struct {
		name string
		line string
		kind rpcMessageKind
	}{
		{
			name: "response",
			line: `{"jsonrpc":"2.0","id":3,"result":{"ok":true}}`,
			kind: rpcMessageResponse,
		},
		{
			name: "notification",
			line: `{"jsonrpc":"2.0","method":"session/update","params":{"value":1}}`,
			kind: rpcMessageNotification,
		},
		{
			name: "server request remains notification path",
			line: `{"jsonrpc":"2.0","id":4,"method":"session/request_permission","params":{}}`,
			kind: rpcMessageNotification,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, kind, err := unmarshalRPCMessage(test.line)
			if err != nil {
				t.Fatal(err)
			}
			if kind != test.kind {
				t.Fatalf("kind=%v, want %v; message=%+v", kind, test.kind, message)
			}
		})
	}
}

func TestUnmarshalRPCMessageRejectsMalformedJSON(t *testing.T) {
	if _, _, err := unmarshalRPCMessage(`{"jsonrpc":`); err == nil {
		t.Fatal("malformed JSON should fail")
	}
}
