package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestACPAgentCodexProgressCallbackReceivesDelta(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{Delta: "实时片段"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	var got []string
	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", func(delta string) {
		got = append(got, delta)
	})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "实时片段" {
		t.Fatalf("reply=%q, want=%q", reply, "实时片段")
	}
	if len(got) != 1 || got[0] != "实时片段" {
		t.Fatalf("progress deltas=%v, want=[实时片段]", got)
	}
}

func TestACPAgentCodexProgressEventDoesNotBecomeFinalReply(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{Kind: "progress", Text: "进展：Codex 已产生代码或文件变更。"}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "最终结果"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	var progress []string
	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", func(delta string) {
		progress = append(progress, delta)
	})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "最终结果" {
		t.Fatalf("reply=%q, want final agent text only", reply)
	}
	if len(progress) != 2 || progress[0] != "进展：Codex 已产生代码或文件变更。" || progress[1] != "最终结果" {
		t.Fatalf("progress=%#v, want status then final delta", progress)
	}
}

func TestACPAgentCodexAssemblerPrefersDeltaOverSnapshot(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{ItemID: "item-1", Text: "你好"}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "你好"}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "，世界"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "你好，世界" {
		t.Fatalf("reply=%q, want=%q", reply, "你好，世界")
	}
}

func TestACPAgentCodexAssemblerUsesSnapshotWhenNoDelta(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{ItemID: "item-1", Text: "完整 snapshot"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "完整 snapshot" {
		t.Fatalf("reply=%q, want=%q", reply, "完整 snapshot")
	}
}

func TestACPAgentCodexAssemblerReturnsLastUserVisibleItem(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})

	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "过程：执行 git status。"}
			ch <- &codexTurnEvent{ItemID: "item-2", Delta: "已完成，最终结果。"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "已完成，最终结果。" {
		t.Fatalf("reply=%q, want only last user visible item", reply)
	}
}

func TestDispatchToTurnChFallbackOnlyWhenSingleActiveTurn(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})

	singleTurnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = singleTurnCh
	a.notifyMu.Unlock()

	a.dispatchToTurnCh("", &codexTurnEvent{Delta: "single"})
	select {
	case evt := <-singleTurnCh:
		if evt.Delta != "single" {
			t.Fatalf("single active fallback event delta=%q, want single", evt.Delta)
		}
	default:
		t.Fatal("single active turn should receive fallback event")
	}

	secondTurnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-2"] = secondTurnCh
	a.notifyMu.Unlock()

	a.dispatchToTurnCh("", &codexTurnEvent{Delta: "multi"})
	select {
	case evt := <-singleTurnCh:
		t.Fatalf("multi active turn should not fallback to thread-1, got %#v", evt)
	default:
	}
	select {
	case evt := <-secondTurnCh:
		t.Fatalf("multi active turn should not fallback to thread-2, got %#v", evt)
	default:
	}
}

func TestHandleCodexDeltaDoesNotFallbackWithMultipleActiveTurns(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	firstTurnCh := make(chan *codexTurnEvent, 1)
	secondTurnCh := make(chan *codexTurnEvent, 1)
	a.notifyMu.Lock()
	a.turnCh["thread-1"] = firstTurnCh
	a.turnCh["thread-2"] = secondTurnCh
	a.notifyMu.Unlock()

	a.handleCodexDelta(json.RawMessage(`{"conversationId":"missing-thread","msg":{"delta":"wrong turn"}}`))

	select {
	case evt := <-firstTurnCh:
		t.Fatalf("unroutable delta should not reach thread-1, got %#v", evt)
	default:
	}
	select {
	case evt := <-secondTurnCh:
		t.Fatalf("unroutable delta should not reach thread-2, got %#v", evt)
	default:
	}
}

func TestCodexInitializeParamsDeclareExperimentalAPI(t *testing.T) {
	params := codexInitializeParams()

	clientInfo, ok := params["clientInfo"].(map[string]string)
	if !ok {
		t.Fatalf("clientInfo type=%T, want map[string]string", params["clientInfo"])
	}
	if clientInfo["name"] != "weclaw" {
		t.Fatalf("clientInfo name=%q, want weclaw", clientInfo["name"])
	}

	caps, ok := params["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("capabilities type=%T, want map[string]interface{}", params["capabilities"])
	}
	if caps["experimentalApi"] != true {
		t.Fatalf("experimentalApi=%v, want true", caps["experimentalApi"])
	}
}
