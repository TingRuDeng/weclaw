package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCodexProgressSnapshotFallsBackForIdleThreadWhenItemsListUnsupported(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir(),
	})
	fullViewCalls := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-idle","status":{"type":"idle"}}}`), nil
		case "thread/turns/list":
			request := params.(map[string]interface{})
			switch request["itemsView"] {
			case "notLoaded":
				return json.RawMessage(`{"data":[{"id":"turn-done","status":"completed","items":[]}],"nextCursor":null}`), nil
			case "full":
				fullViewCalls++
				return json.RawMessage(`{"data":[{"id":"turn-done","status":"completed","items":[{"id":"message-1","type":"agentMessage","phase":"final_answer","text":"任务已经完成"}]}],"itemsView":"full","nextCursor":null}`), nil
			default:
				t.Fatalf("unexpected itemsView=%v", request["itemsView"])
			}
		case "thread/items/list":
			return nil, fmt.Errorf("agent error: thread/items/list is not supported yet")
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return nil, fmt.Errorf("unreachable")
	}

	state, progress, err := a.ReadCodexThreadProgressSnapshot(context.Background(), "conversation-1", "thread-idle")
	if err != nil {
		t.Fatalf("ReadCodexThreadProgressSnapshot() error=%v", err)
	}
	if state.Active || len(progress) != 0 || fullViewCalls != 1 || state.LastAgentMessageText != "任务已经完成" {
		t.Fatalf("state=%#v progress=%#v fullViewCalls=%d, want idle final answer from full view", state, progress, fullViewCalls)
	}
}

func TestCodexProgressSnapshotFallsBackToFullTurnViewWhenItemsListUnsupported(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir(),
	})
	fullViewCalls := 0
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		request := params.(map[string]interface{})
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-active","status":{"type":"active"}}}`), nil
		case "thread/turns/list":
			switch request["itemsView"] {
			case "notLoaded":
				return json.RawMessage(`{"data":[{"id":"turn-active","status":"inProgress","items":[]}],"nextCursor":null}`), nil
			case "full":
				fullViewCalls++
				return json.RawMessage(`{"data":[{"id":"turn-active","status":"inProgress","items":[{"id":"message-1","type":"agentMessage","phase":"commentary","text":"恢复后的进度"}]}],"itemsView":"full","nextCursor":null}`), nil
			default:
				t.Fatalf("unexpected itemsView=%v", request["itemsView"])
			}
		case "thread/items/list":
			return nil, fmt.Errorf("agent error: thread/items/list is not supported yet")
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return nil, fmt.Errorf("unreachable")
	}

	state, progress, err := a.ReadCodexThreadProgressSnapshot(context.Background(), "conversation-1", "thread-active")
	if err != nil {
		t.Fatalf("ReadCodexThreadProgressSnapshot() error=%v", err)
	}
	if !state.Active || state.ActiveTurnID != "turn-active" || fullViewCalls != 1 {
		t.Fatalf("state=%#v fullViewCalls=%d, want active turn with one full-view fallback", state, fullViewCalls)
	}
	if len(progress) != 1 || strings.TrimSpace(progress[0].DisplayText()) != "恢复后的进度" {
		t.Fatalf("progress=%#v, want recovered commentary", progress)
	}
}

func TestCodexFullTurnFallbackReusesTargetMetadataPageCursor(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		request := params.(map[string]interface{})
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-active","status":{"type":"active"}}}`), nil
		case "thread/turns/list":
			itemsView, _ := request["itemsView"].(string)
			cursor, _ := request["cursor"].(string)
			switch itemsView {
			case "notLoaded":
				if cursor == "" {
					return json.RawMessage(`{"data":[{"id":"turn-new","status":"inProgress","items":[]}],"nextCursor":"turns-2"}`), nil
				}
				if cursor == "turns-2" {
					return json.RawMessage(`{"data":[{"id":"turn-target","status":"inProgress","items":[]}],"nextCursor":null}`), nil
				}
			case "full":
				if cursor != "turns-2" {
					return nil, fmt.Errorf("full turn fallback cursor=%q, want turns-2", cursor)
				}
				return json.RawMessage(`{"data":[{"id":"turn-target","status":"inProgress","items":[{"id":"message-1","type":"agentMessage","phase":"commentary","text":"目标页进度"}]}],"itemsView":"full","nextCursor":null}`), nil
			}
			return nil, fmt.Errorf("unexpected turn page itemsView=%q cursor=%q", itemsView, cursor)
		case "thread/items/list":
			return nil, fmt.Errorf("agent error: thread/items/list is not supported yet")
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}

	state, snapshot, _, _, err := a.readCodexAppServerThreadSnapshotResult(context.Background(), "thread-active", "turn-target")
	if err != nil {
		t.Fatalf("readCodexAppServerThreadSnapshotResult() error=%v", err)
	}
	if !state.Active || state.ActiveTurnID != "turn-target" || len(snapshot.Turns) != 1 || len(snapshot.Turns[0].Items) != 1 {
		t.Fatalf("state=%#v snapshot=%#v, want target turn from its full-view page", state, snapshot)
	}
}

func TestCodexFullTurnFallbackRejectsLatestTurnChangingBetweenReads(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"}, Cwd: t.TempDir(),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		request := params.(map[string]interface{})
		switch method {
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"thread-active","status":{"type":"active"}}}`), nil
		case "thread/turns/list":
			switch request["itemsView"] {
			case "notLoaded":
				return json.RawMessage(`{"data":[{"id":"turn-original","status":"inProgress","items":[]}],"nextCursor":null}`), nil
			case "full":
				return json.RawMessage(`{"data":[{"id":"turn-new","status":"inProgress","items":[{"id":"message-new","type":"agentMessage","phase":"commentary","text":"不应被接受"}]}],"itemsView":"full","nextCursor":null}`), nil
			default:
				t.Fatalf("unexpected itemsView=%v", request["itemsView"])
			}
		case "thread/items/list":
			return nil, fmt.Errorf("agent error: thread/items/list is not supported yet")
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return nil, fmt.Errorf("unreachable")
	}

	_, _, _, _, err := a.readCodexAppServerThreadSnapshotResult(context.Background(), "thread-active", "")
	if err == nil || !errors.Is(err, ErrCodexControlChanged) {
		t.Fatalf("error=%v, want target-turn change rejection", err)
	}
}
