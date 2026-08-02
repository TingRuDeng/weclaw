package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestCodexProgressStateKeepsCommandSemanticAndDropsOutput(t *testing.T) {
	state := newCodexProgressState()

	got, ok := state.record(&codexTurnEvent{
		Kind: "progress",
		Text: "运行测试",
		Progress: &codexProgressEvent{
			ID:     "command:test",
			Kind:   "command",
			Action: "运行测试",
		},
	})
	if !ok || got != "进展：运行测试" {
		t.Fatalf("progress=%q ok=%v, want command action", got, ok)
	}

	got, ok = state.record(&codexTurnEvent{
		Kind: "progress",
		Text: "ok github.com/fastclaw-ai/weclaw/agent 0.231s",
		Progress: &codexProgressEvent{
			Kind:   "command",
			Detail: "ok github.com/fastclaw-ai/weclaw/agent 0.231s",
		},
	})
	if ok || got != "" {
		t.Fatalf("raw command output leaked into progress=%q ok=%v", got, ok)
	}
}

func TestCodexProgressStateCountsChangedFiles(t *testing.T) {
	state := newCodexProgressState()

	state.record(&codexTurnEvent{
		Kind: "progress",
		Text: "修改 agent/a.go",
		Progress: &codexProgressEvent{
			Kind:     "file",
			Action:   "修改 agent/a.go",
			FilePath: "agent/a.go",
		},
	})
	got, ok := state.record(&codexTurnEvent{
		Kind: "progress",
		Text: "修改 agent/b.go",
		Progress: &codexProgressEvent{
			Kind:     "file",
			Action:   "修改 agent/b.go",
			FilePath: "agent/b.go",
		},
	})

	want := "进展：修改代码 · 已变更 2 个文件"
	if !ok || got != want {
		t.Fatalf("progress=%q ok=%v, want %q", got, ok, want)
	}
}

func TestCodexProgressStateCarriesStructuredMetadata(t *testing.T) {
	state := newCodexProgressState()
	event, ok := state.recordEvent(&codexTurnEvent{
		Kind: "progress", Sequence: 19, ItemID: "fallback-item",
		Text: "修改代码",
		Progress: &codexProgressEvent{
			ID: "file:changes", Kind: "file", Status: "completed",
			Action: "修改代码", FilePath: "messaging/task_state.go",
		},
	})
	if !ok {
		t.Fatal("structured file progress must emit")
	}
	if event.ID != "file:changes" || event.Kind != ProgressKindFile || event.State != ProgressStateCompleted || event.Sequence != 19 {
		t.Fatalf("event=%#v", event)
	}
	if event.Path != "messaging/task_state.go" || event.DisplayText() != "进展：修改代码 · 已变更 1 个文件" {
		t.Fatalf("path=%q display=%q", event.Path, event.DisplayText())
	}
}

func TestCodexProgressStateCarriesToolMetadata(t *testing.T) {
	state := newCodexProgressState()
	event, ok := state.recordEvent(&codexTurnEvent{
		Kind: "progress", Sequence: 23,
		Text: "分析项目",
		Progress: &codexProgressEvent{
			ID: "tool-item", Kind: "tool", Status: "completed",
			Action: "分析项目",
		},
	})
	if !ok {
		t.Fatal("structured tool progress must emit")
	}
	if event.ID != "tool-item" || event.Kind != ProgressKindTool || event.State != ProgressStateCompleted || event.Sequence != 23 {
		t.Fatalf("event=%#v", event)
	}
}

func TestACPAgentCodexTurnAggregatesCommandProgress(t *testing.T) {
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
			ch <- &codexTurnEvent{
				Kind: "progress",
				Text: "运行测试",
				Progress: &codexProgressEvent{
					ID:     "command:test",
					Kind:   "command",
					Action: "运行测试",
				},
			}
			ch <- &codexTurnEvent{
				Kind: "progress",
				Text: "ok github.com/fastclaw-ai/weclaw/agent 0.231s",
				Progress: &codexProgressEvent{
					Kind:   "command",
					Detail: "ok github.com/fastclaw-ai/weclaw/agent 0.231s",
				},
			}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "最终回复"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	createCodexThreadForTest(t, ctx, a, "user-1")
	var progress []string
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello", onProgress: func(delta string) {
		progress = append(progress, delta)
	}})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "最终回复" {
		t.Fatalf("reply=%q, want final reply", reply)
	}
	want := []string{
		"进展：运行测试",
	}
	if fmt.Sprint(progress) != fmt.Sprint(want) {
		t.Fatalf("progress=%#v, want %#v", progress, want)
	}
}
