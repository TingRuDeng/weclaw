package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRunCodexTurnConfirmsUnknownStartDeliveryFromHistory(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	var mu sync.Mutex
	accepted := false
	startCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "thread/read":
			status := "idle"
			if accepted {
				status = "active"
			}
			return json.RawMessage(fmt.Sprintf(`{"thread":{"id":"thread-1","status":{"type":%q,"activeFlags":[]}}}`, status)), nil
		case "thread/turns/list":
			if accepted {
				return json.RawMessage(`{"data":[{"id":"turn-new","status":"inProgress","items":[]}],"nextCursor":null}`), nil
			}
			return json.RawMessage(`{"data":[{"id":"turn-old","status":"completed","items":[]}],"nextCursor":null}`), nil
		case "thread/items/list":
			return json.RawMessage(`{"data":[{"turnId":"turn-new","item":{"id":"user-new","type":"userMessage","text":"执行唯一任务"}}],"nextCursor":null}`), nil
		case "turn/start":
			startCalls++
			accepted = true
			return nil, ErrCodexInputDeliveryUnknown
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}
	dispatchCodexRuntimeTestCompletion(a, "thread-1", "turn-new", "已确认并完成")

	reply, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "执行唯一任务",
	})

	if err != nil || reply != "已确认并完成" || startCalls != 1 {
		t.Fatalf("reply=%q startCalls=%d err=%v", reply, startCalls, err)
	}
}

func TestRunCodexTurnDoesNotRetryUnknownStartWithoutEvidence(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	startCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-old","status":"completed","items":[]}],"nextCursor":null}`), nil
		case "thread/items/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/start":
			startCalls++
			return nil, ErrCodexInputDeliveryUnknown
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "不要重复执行",
	})

	if !errors.Is(err, ErrCodexInputDeliveryUnconfirmed) || startCalls != 1 {
		t.Fatalf("error=%v startCalls=%d, want unconfirmed and exactly one write", err, startCalls)
	}
}

func TestRunCodexTurnConfirmsUnknownSteerDeliveryFromNewUserItem(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	var mu sync.Mutex
	accepted := false
	steerCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":[]}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-live","status":"inProgress","items":[]}],"nextCursor":null}`), nil
		case "thread/items/list":
			if accepted {
				return json.RawMessage(`{"data":[{"turnId":"turn-live","item":{"id":"user-extra","type":"userMessage","text":"补充唯一约束"}}],"nextCursor":null}`), nil
			}
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/steer":
			steerCalls++
			accepted = true
			return nil, ErrCodexInputDeliveryUnknown
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}
	dispatchCodexRuntimeTestCompletion(a, "thread-1", "turn-live", "补充已生效")

	reply, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "补充唯一约束",
	})

	if err != nil || reply != "补充已生效" || steerCalls != 1 {
		t.Fatalf("reply=%q steerCalls=%d err=%v", reply, steerCalls, err)
	}
}

func TestSteerCodexInputReturnsNoActiveTurnWithoutStarting(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	writeCalls := 0
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-old","status":"completed","items":[]}],"nextCursor":null}`), nil
		case "thread/items/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/start", "turn/steer":
			writeCalls++
			return nil, fmt.Errorf("unexpected write %s", method)
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	_, err := a.SteerCodexInput(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "仅在活动任务中补充",
	})

	if !errors.Is(err, ErrCodexNoActiveTurn) || writeCalls != 0 {
		t.Fatalf("error=%v writeCalls=%d, want no-active without write", err, writeCalls)
	}
}

func TestSteerCodexInputConfirmsUnknownDeliveryWithoutStartingObserver(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	accepted := false
	steerCalls := 0
	var statuses []CodexInputAttemptStatus
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":[]}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-live","status":"inProgress","items":[]}],"nextCursor":null}`), nil
		case "thread/items/list":
			if accepted {
				return json.RawMessage(`{"data":[{"turnId":"turn-live","item":{"id":"user-extra","type":"userMessage","text":"补充约束"}}],"nextCursor":null}`), nil
			}
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/steer":
			steerCalls++
			accepted = true
			return nil, ErrCodexInputDeliveryUnknown
		case "turn/start":
			return nil, fmt.Errorf("unexpected turn/start")
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	turnID, err := a.SteerCodexInput(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "补充约束", AttemptID: "attempt-1",
		OnInputAttempt: func(attempt CodexInputAttempt) error {
			statuses = append(statuses, attempt.Status)
			return nil
		},
	})

	if err != nil || turnID != "turn-live" || steerCalls != 1 {
		t.Fatalf("turnID=%q steerCalls=%d error=%v", turnID, steerCalls, err)
	}
	if !reflect.DeepEqual(statuses, []CodexInputAttemptStatus{
		CodexInputAttemptPending, CodexInputAttemptAccepted,
	}) {
		t.Fatalf("statuses=%v", statuses)
	}
}

func TestRunCodexTurnRecordsRejectedWhenStartDefinitelyFails(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	var statuses []CodexInputAttemptStatus
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/start":
			return nil, errors.New("permission denied")
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	_, err := a.RunCodexTurn(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "不会被接收", AttemptID: "attempt-start-rejected",
		OnInputAttempt: func(attempt CodexInputAttempt) error {
			statuses = append(statuses, attempt.Status)
			return nil
		},
	})

	if err == nil {
		t.Fatal("expected deterministic start failure")
	}
	if !reflect.DeepEqual(statuses, []CodexInputAttemptStatus{
		CodexInputAttemptPending, CodexInputAttemptRejected,
	}) {
		t.Fatalf("statuses=%v", statuses)
	}
}

func TestSteerCodexInputRecordsRejectedWhenSteerDefinitelyFails(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"},
		StateFile: filepath.Join(t.TempDir(), "state.json"),
	})
	request := remoteCodexRuntimeRequest("thread-1", "route-1", 1)
	a.threads[request.Ref.ConversationID] = request.Ref.ThreadID
	var statuses []CodexInputAttemptStatus
	a.rpcCall = func(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":[]}}}`), nil
		case "thread/turns/list":
			return json.RawMessage(`{"data":[{"id":"turn-live","status":"inProgress","items":[]}],"nextCursor":null}`), nil
		case "thread/items/list":
			return json.RawMessage(`{"data":[],"nextCursor":null}`), nil
		case "turn/steer":
			return nil, errors.New("permission denied")
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	_, err := a.SteerCodexInput(context.Background(), CodexTurnRequest{
		Runtime: request, Message: "不会被接收", AttemptID: "attempt-steer-rejected",
		OnInputAttempt: func(attempt CodexInputAttempt) error {
			statuses = append(statuses, attempt.Status)
			return nil
		},
	})

	if err == nil {
		t.Fatal("expected deterministic steer failure")
	}
	if !reflect.DeepEqual(statuses, []CodexInputAttemptStatus{
		CodexInputAttemptPending, CodexInputAttemptRejected,
	}) {
		t.Fatalf("statuses=%v", statuses)
	}
}
