package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestACPAgentRenamesIdleCodexThreadAndReadsBackName(t *testing.T) {
	a := newCodexRenameTestAgent(t)
	var methods []string
	readCount := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		methods = append(methods, method)
		values, ok := params.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s params=%#v", method, params)
		}
		switch method {
		case "thread/read":
			if !reflect.DeepEqual(values, map[string]interface{}{"threadId": "thread-rename"}) {
				return nil, fmt.Errorf("thread/read params=%#v", values)
			}
			readCount++
			name := "旧名称"
			if readCount == 2 {
				name = "新 名称"
			}
			return json.RawMessage(fmt.Sprintf(`{"thread":{"id":"thread-rename","name":%q,"status":{"type":"idle"}}}`, name)), nil
		case "thread/name/set":
			if !reflect.DeepEqual(values, map[string]interface{}{"threadId": "thread-rename", "name": "新 名称"}) {
				return nil, fmt.Errorf("thread/name/set params=%#v", values)
			}
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	if err := a.RenameCodexThread(context.Background(), " thread-rename ", "新 名称"); err != nil {
		t.Fatalf("RenameCodexThread error: %v", err)
	}
	if !reflect.DeepEqual(methods, []string{"thread/read", "thread/name/set", "thread/read"}) {
		t.Fatalf("rpc methods=%v", methods)
	}
}

func TestACPAgentCodexRenameRejectsActiveThreadBeforeMutation(t *testing.T) {
	a := newCodexRenameTestAgent(t)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "thread/read" {
			t.Fatalf("unexpected mutation rpc: %s", method)
		}
		return json.RawMessage(`{"thread":{"id":"thread-rename","name":"旧名称","status":{"type":"active"}}}`), nil
	}

	err := a.RenameCodexThread(context.Background(), "thread-rename", "新名称")
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("rename error=%v, want ErrCodexWriterBusy", err)
	}
}

func TestACPAgentCodexRenameRejectsWriterLeaseBeforeRPC(t *testing.T) {
	a := newCodexRenameTestAgent(t)
	a.codexOwners.mu.Lock()
	a.codexOwners.leases["thread-rename"] = &codexWriterLeaseState{}
	a.codexOwners.mu.Unlock()
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		t.Fatalf("writer lease should reject before rpc: %s", method)
		return nil, nil
	}

	err := a.RenameCodexThread(context.Background(), "thread-rename", "新名称")
	if !errors.Is(err, ErrCodexWriterBusy) {
		t.Fatalf("rename error=%v, want ErrCodexWriterBusy", err)
	}
}

func TestACPAgentCodexRenameRejectsDesktopFollower(t *testing.T) {
	a := newCodexRenameTestAgent(t)
	a.setCodexRuntimeMode(CodexRuntimeDesktop)
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		t.Fatalf("Desktop follower should reject before rpc: %s", method)
		return nil, nil
	}

	err := a.RenameCodexThread(context.Background(), "thread-rename", "新名称")
	if !errors.Is(err, ErrCodexDesktopCapabilityUnavailable) {
		t.Fatalf("rename error=%v, want ErrCodexDesktopCapabilityUnavailable", err)
	}
}

func TestACPAgentCodexRenameReadbackMismatchIsUnknownOutcome(t *testing.T) {
	a := newCodexRenameTestAgent(t)
	readCount := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			readCount++
			return json.RawMessage(`{"thread":{"id":"thread-rename","name":"旧名称","status":{"type":"idle"}}}`), nil
		case "thread/name/set":
			return json.RawMessage(`{}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	err := a.RenameCodexThread(context.Background(), "thread-rename", "新名称")
	if !errors.Is(err, ErrCodexRenameOutcomeUnknown) {
		t.Fatalf("rename error=%v, want ErrCodexRenameOutcomeUnknown (reads=%d)", err, readCount)
	}
}

func TestACPAgentCodexRenameRejectsInvalidNameBeforeRPC(t *testing.T) {
	for _, name := range []string{" 前导空白", "两行\n名称", strings.Repeat("名", 121)} {
		a := newCodexRenameTestAgent(t)
		a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
			t.Fatalf("invalid name should reject before rpc: %s", method)
			return nil, nil
		}
		if err := a.RenameCodexThread(context.Background(), "thread-rename", name); err == nil {
			t.Fatalf("RenameCodexThread name=%q succeeded, want validation error", name)
		}
	}
}

func newCodexRenameTestAgent(t *testing.T) *ACPAgent {
	t.Helper()
	return NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir(),
	})
}
