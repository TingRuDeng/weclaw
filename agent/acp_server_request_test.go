package agent

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestACPAgentRespondsToUnsupportedDynamicToolCall(t *testing.T) {
	a, output := newACPServerRequestTestAgent(t)

	a.handleACPWireLine(`{"jsonrpc":"2.0","id":60,"method":"item/tool/call","params":{"tool":"list_projects","arguments":{}}}`)

	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Result  struct {
			ContentItems []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"contentItems"`
			Success *bool `json:"success"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}
	decodeACPServerResponse(t, output, &response)
	if response.JSONRPC != "2.0" || response.ID != 60 {
		t.Fatalf("response identity=%+v", response)
	}
	if response.Error != nil {
		t.Fatalf("error=%+v, want dynamic tool failure result", response.Error)
	}
	if response.Result.Success == nil || *response.Result.Success {
		t.Fatalf("success=%v, want explicit false", response.Result.Success)
	}
	if len(response.Result.ContentItems) != 1 ||
		response.Result.ContentItems[0].Type != "inputText" ||
		!strings.Contains(response.Result.ContentItems[0].Text, "WeClaw") {
		t.Fatalf("contentItems=%+v", response.Result.ContentItems)
	}
}

func TestACPAgentRejectsUnknownServerRequest(t *testing.T) {
	a, output := newACPServerRequestTestAgent(t)

	a.handleACPWireLine(`{"jsonrpc":"2.0","id":61,"method":"future/request","params":{}}`)

	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	decodeACPServerResponse(t, output, &response)
	if response.JSONRPC != "2.0" || response.ID != 61 {
		t.Fatalf("response identity=%+v", response)
	}
	if response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("error=%+v, want JSON-RPC method-not-found", response.Error)
	}
	if len(response.Result) != 0 {
		t.Fatalf("result=%s, want no result", response.Result)
	}
}

func newACPServerRequestTestAgent(t *testing.T) (*ACPAgent, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server"},
		Cwd:       root,
		StateFile: filepath.Join(root, "state.json"),
	})
	output := &bytes.Buffer{}
	a.mu.Lock()
	a.stdin = acpServerRequestTestWriteCloser{Buffer: output}
	a.mu.Unlock()
	t.Cleanup(func() {
		a.mu.Lock()
		a.stdin = nil
		a.mu.Unlock()
	})
	return a, output
}

func decodeACPServerResponse(t *testing.T, output *bytes.Buffer, response interface{}) {
	t.Helper()
	data := bytes.TrimSpace(output.Bytes())
	if len(data) == 0 {
		t.Fatal("server request received no JSON-RPC response")
	}
	if err := json.Unmarshal(data, response); err != nil {
		t.Fatalf("response json: %v; raw=%s", err, data)
	}
}

type acpServerRequestTestWriteCloser struct {
	*bytes.Buffer
}

func (acpServerRequestTestWriteCloser) Close() error {
	return nil
}
