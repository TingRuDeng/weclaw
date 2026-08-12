package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestACPAgentCodexProgressCallbackIgnoresSyntheticStatus(t *testing.T) {
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
			ch <- &codexTurnEvent{Kind: "progress", Text: "进展：Codex 正在分析请求。"}
			ch <- &codexTurnEvent{Delta: "最终回复"}
			ch <- &codexTurnEvent{Kind: "completed"}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	createCodexThreadForTest(t, ctx, a, "user-1")
	var got []string
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello", onProgress: func(delta string) {
		got = append(got, delta)
	}})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "最终回复" {
		t.Fatalf("reply=%q, want=%q", reply, "最终回复")
	}
	if len(got) != 0 {
		t.Fatalf("synthetic status leaked into progress: %v", got)
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

	createCodexThreadForTest(t, ctx, a, "user-1")
	var progress []string
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello", onProgress: func(delta string) {
		progress = append(progress, delta)
	}})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "最终结果" {
		t.Fatalf("reply=%q, want final agent text only", reply)
	}
	if len(progress) != 0 {
		t.Fatalf("synthetic status leaked into progress: %#v", progress)
	}
}

func TestACPAgentCodexStructuredLifecycleSkipsCommandProgress(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "acp-state.json"),
	})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)
			a.handleCodexItemStartedAt(json.RawMessage(`{
				"threadId":"thread-1",
				"item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","status":"inProgress"}
			}`), 5)
			a.handleCodexItemCompletedAt(json.RawMessage(`{
				"threadId":"thread-1",
				"item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","aggregatedOutput":"private output","status":"completed"}
			}`), 6)
			a.handleCodexItemStartedAt(json.RawMessage(`{
				"threadId":"thread-1",
				"item":{"id":"file-1","type":"fileChange","changes":[{"path":"/private/workspace/file.go"}],"status":"inProgress"}
			}`), 7)
			a.handleCodexItemCompletedAt(json.RawMessage(`{
				"threadId":"thread-1",
				"item":{"id":"file-1","type":"fileChange","changes":[{"path":"/private/workspace/file.go","diff":"private diff"}],"status":"completed"}
			}`), 8)
			a.handleCodexItemCompletedAt(json.RawMessage(`{
				"threadId":"thread-1",
				"item":{"id":"message-1","type":"agentMessage","phase":"final_answer","text":"最终结果"}
			}`), 9)
			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			ch <- &codexTurnEvent{Kind: "completed", Sequence: 10}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	createCodexThreadForTest(t, ctx, a, "user-1")
	var progress []ProgressEvent
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{
		ctx: ctx, conversationID: "user-1", message: "hello",
		onProgressEvent: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil || reply != "最终结果" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if len(progress) != 2 || progress[0].ID != "file:file-1" || progress[1].ID != progress[0].ID ||
		progress[0].State != ProgressStateRunning || progress[1].State != ProgressStateCompleted {
		t.Fatalf("progress=%#v", progress)
	}
	for _, event := range progress {
		if event.Text != "修改文件" || strings.Contains(event.Text, "/private/workspace") || strings.Contains(event.Text, "private diff") {
			t.Fatalf("unsafe progress=%#v", event)
		}
	}
}

func TestACPAgentCodexDeltaDoesNotEmitGenericProgress(t *testing.T) {
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
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "最终"}
			ch <- &codexTurnEvent{ItemID: "item-1", Delta: "回复"}
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
		t.Fatalf("reply=%q, want final agent text only", reply)
	}
	if len(progress) != 0 {
		t.Fatalf("delta leaked into progress: %#v", progress)
	}
}

func TestACPAgentCodexCommentaryBecomesProgressButFinalAnswerDoesNot(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "acp-state.json"),
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
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-1", MessagePhase: "commentary", Sequence: 11, Text: "我先检查当前实现。"}
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-2", MessagePhase: "final_answer", Sequence: 12, Text: "已经完成修复。"}
			ch <- &codexTurnEvent{Kind: "completed", Sequence: 13}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	createCodexThreadForTest(t, ctx, a, "user-1")
	var progress []ProgressEvent
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{
		ctx: ctx, conversationID: "user-1", message: "hello",
		onProgressEvent: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "已经完成修复。" {
		t.Fatalf("reply=%q", reply)
	}
	if len(progress) != 1 {
		t.Fatalf("progress=%#v", progress)
	}
	if progress[0].Kind != ProgressKindCommentary || progress[0].Text != "我先检查当前实现。" || progress[0].Sequence != 11 {
		t.Fatalf("first progress=%#v", progress[0])
	}
}

func TestACPAgentCodexUnphasedIntermediateMessagesBecomeProgressButLastStaysFinal(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "acp-state.json"),
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
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-1", Sequence: 11, Text: "我先检查当前实现。"}
			ch <- &codexTurnEvent{Kind: "progress", Sequence: 12, Progress: &ProgressEvent{
				ID: "file:file-1", Kind: ProgressKindFile, State: ProgressStateCompleted,
				Sequence: 12, Text: "修改进度实现",
			}}
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-2", Sequence: 13, Text: "我继续运行回归测试。"}
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-3", Sequence: 14, Text: "已经完成修复。"}
			ch <- &codexTurnEvent{Kind: "completed", Sequence: 15}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	createCodexThreadForTest(t, ctx, a, "user-1")
	var progress []ProgressEvent
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{
		ctx: ctx, conversationID: "user-1", message: "hello",
		onProgressEvent: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "已经完成修复。" {
		t.Fatalf("reply=%q", reply)
	}
	if len(progress) != 3 {
		t.Fatalf("progress=%#v, want two intermediate messages and one file event", progress)
	}
	if progress[0].ID != "agent-message:message-1" || progress[0].Kind != ProgressKindCommentary ||
		progress[0].Text != "我先检查当前实现。" {
		t.Fatalf("first progress=%#v", progress[0])
	}
	if progress[1].ID != "file:file-1" || progress[2].ID != "agent-message:message-2" ||
		progress[2].Text != "我继续运行回归测试。" {
		t.Fatalf("progress=%#v", progress)
	}
	for _, event := range progress {
		if strings.Contains(event.Text, "已经完成修复") {
			t.Fatalf("final answer entered progress: %#v", progress)
		}
	}
}

func TestCollectAttachedCodexUnphasedIntermediateMessageBecomesProgressButLastStaysFinal(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 4)
	turnCh <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-1", Sequence: 21, Text: "我先核对恢复任务。"}
	turnCh <- &codexTurnEvent{Kind: "progress", Sequence: 22, Progress: &ProgressEvent{
		ID: "file:file-1", Kind: ProgressKindFile, State: ProgressStateCompleted,
		Sequence: 22, Text: "修改恢复逻辑",
	}}
	turnCh <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-2", Sequence: 23, Text: "恢复任务已经完成。"}
	turnCh <- &codexTurnEvent{Kind: "completed", Sequence: 24}

	var progress []ProgressEvent
	reply, err := a.collectAttachedCodexTurn(context.Background(), codexThreadWatchOptions{
		conversationID: "user-1", threadID: "thread-1", turnCh: turnCh,
		onProgressEvent: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil {
		t.Fatalf("collectAttachedCodexTurn error: %v", err)
	}
	if reply != "恢复任务已经完成。" {
		t.Fatalf("reply=%q", reply)
	}
	if len(progress) != 2 || progress[0].ID != "agent-message:message-1" ||
		progress[0].Kind != ProgressKindCommentary || progress[1].ID != "file:file-1" {
		t.Fatalf("progress=%#v, want intermediate message followed by file event", progress)
	}
}

func TestCollectAttachedCodexSkipsLiveItemsAlreadyIncludedInAppServerSnapshot(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 2)
	turnCh <- &codexTurnEvent{
		Kind: "item_delta", ItemID: "message-final", MessagePhase: "final_answer",
		Sequence: 40, Delta: "后缀",
	}
	turnCh <- &codexTurnEvent{Kind: "completed", Sequence: 42}

	reply, err := a.collectAttachedCodexTurn(context.Background(), codexThreadWatchOptions{
		conversationID: "user-1", threadID: "thread-1", turnCh: turnCh,
		appServerSequence: 41,
		initialEvents: []*codexTurnEvent{{
			Kind: "item_completed", ItemID: "message-final", MessagePhase: "final_answer",
			Text: "完整最终回答",
		}},
	})
	if err != nil {
		t.Fatalf("collectAttachedCodexTurn error: %v", err)
	}
	if reply != "完整最终回答" {
		t.Fatalf("reply=%q, want complete snapshot answer", reply)
	}
}

func TestAppServerSnapshotWatermarkKeepsControlEvents(t *testing.T) {
	approval := &codexTurnEvent{
		Kind: "approval_request", Sequence: 40,
		Approval: &codexApprovalRequest{Request: ApprovalRequest{RequestID: "approval-1"}},
	}
	userInput := &codexTurnEvent{
		Kind: "user_input_request", Sequence: 40,
		UserInput: &codexUserInputEvent{Request: UserInputRequest{RequestID: "input-1"}},
	}
	for _, event := range []*codexTurnEvent{approval, userInput, {Kind: "completed", Sequence: 40}} {
		if shouldSkipCodexAppServerSnapshotDuplicate(event, 41) {
			t.Fatalf("control event was hidden by snapshot watermark: %#v", event)
		}
	}
	structured := &codexTurnEvent{
		Kind: "progress", Sequence: 40,
		Progress: &ProgressEvent{ID: "file:file-1", Kind: ProgressKindFile, Text: "修改文件"},
	}
	if shouldSkipCodexAppServerSnapshotDuplicate(structured, 41) {
		t.Fatalf("structured progress missing from thread snapshot was hidden: %#v", structured)
	}
}

func TestACPAgentCodexUnphasedMessageUpdateWithSameIDEmitsLatestTextOnce(t *testing.T) {
	ctx := context.Background()
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server", "--listen", "stdio://"},
		Cwd: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "acp-state.json"),
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
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-1", Sequence: 31, Text: "尚未完成的说明"}
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-1", Sequence: 32, Text: "更新后的完整说明"}
			ch <- &codexTurnEvent{Kind: "progress", Sequence: 33, Progress: &ProgressEvent{
				ID: "file:file-1", Kind: ProgressKindFile, State: ProgressStateCompleted,
				Sequence: 33, Text: "修改进度实现",
			}}
			ch <- &codexTurnEvent{Kind: "item_completed", ItemID: "message-2", Sequence: 34, Text: "最终结果"}
			ch <- &codexTurnEvent{Kind: "completed", Sequence: 35}
			return json.RawMessage(`{"ok":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method: %s", method)
		}
	}

	createCodexThreadForTest(t, ctx, a, "user-1")
	var progress []ProgressEvent
	_, err := a.chatCodexAppServer(codexAppServerTurnOptions{
		ctx: ctx, conversationID: "user-1", message: "hello",
		onProgressEvent: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if len(progress) != 2 || progress[0].ID != "agent-message:message-1" ||
		progress[0].Text != "更新后的完整说明" || progress[1].ID != "file:file-1" {
		t.Fatalf("progress=%#v, want latest message text exactly once", progress)
	}
}

func TestCodexNativeMessageProgressRejectsUnknownAndFinalPhases(t *testing.T) {
	for _, phase := range []string{"", "final_answer", "future_phase"} {
		if event, ok := codexNativeMessageProgressEvent(&codexTurnEvent{
			Kind: "item_completed", ItemID: "message-1", MessagePhase: phase, Text: "不得进入进度卡",
		}); ok {
			t.Fatalf("phase=%q event=%#v, want hidden", phase, event)
		}
	}
}

func TestCodexNativeMessageProgressPreservesEveryCommentaryItem(t *testing.T) {
	inputs := []*codexTurnEvent{
		{Kind: "item_completed", ItemID: "message-1", MessagePhase: "commentary", Sequence: 11, Text: "第一段说明"},
		{Kind: "item_completed", ItemID: "message-2", MessagePhase: "commentary", Sequence: 12, Text: "第二段说明"},
	}
	for index, input := range inputs {
		event, ok := codexNativeMessageProgressEvent(input)
		if !ok {
			t.Fatalf("commentary %d was not projected", index)
		}
		if event.Kind != ProgressKindCommentary || event.ID != "agent-message:"+input.ItemID ||
			event.Sequence != input.Sequence || event.Text != input.Text {
			t.Fatalf("event %d=%#v", index, event)
		}
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

	createCodexThreadForTest(t, ctx, a, "user-1")
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"})
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

	createCodexThreadForTest(t, ctx, a, "user-1")
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"})
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

	createCodexThreadForTest(t, ctx, a, "user-1")
	reply, err := a.chatCodexAppServer(codexAppServerTurnOptions{ctx: ctx, conversationID: "user-1", message: "hello"})
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

func TestDispatchToTurnChReservesCapacityForCompletedEvent(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	turnCh := make(chan *codexTurnEvent, 16)
	a.turnCh["thread-1"] = turnCh
	for i := 0; i < cap(turnCh)*2; i++ {
		a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "progress", Text: "running"})
	}
	if !a.dispatchToTurnCh("thread-1", &codexTurnEvent{Kind: "completed"}) {
		t.Fatal("completed event was dropped")
	}

	found := false
	for len(turnCh) > 0 {
		if (<-turnCh).Kind == "completed" {
			found = true
		}
	}
	if !found {
		t.Fatal("completed event missing from turn channel")
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
