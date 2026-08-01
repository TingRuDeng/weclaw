package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestActiveTaskProgressSnapshotRejectsStaleAndLateEvents(t *testing.T) {
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "codex"})
	now := time.Now()
	latest := agent.ProgressEvent{
		ID: "command-2", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning,
		Sequence: 20, Summary: "运行测试", Text: "进展：运行 go test ./messaging",
	}
	if display, ok := task.recordProgress(now, latest); !ok || display != latest.Text {
		t.Fatalf("latest=(%q,%v)", display, ok)
	}
	if _, ok := task.recordProgress(now.Add(time.Second), agent.ProgressEvent{
		Sequence: 19, Kind: agent.ProgressKindFile, Text: "进展：过期文件事件",
	}); ok {
		t.Fatal("stale source sequence must not replace the snapshot")
	}
	if _, ok := task.recordProgressText(now.Add(time.Second), "进展：晚到的旧字符串事件"); ok {
		t.Fatal("unsequenced legacy progress must not replace a sequenced snapshot")
	}
	task.closeProgress()
	if _, ok := task.recordProgress(now.Add(2*time.Second), agent.ProgressEvent{
		Sequence: 21, Kind: agent.ProgressKindFile, Text: "进展：晚到文件事件",
	}); ok {
		t.Fatal("late progress must not pass the terminal watermark")
	}

	task.mu.Lock()
	defer task.mu.Unlock()
	if task.view.lastProgress != latest.Text || task.view.lastProgressEvent.ID != latest.ID || task.view.revision != 1 {
		t.Fatalf("snapshot=%q event=%#v revision=%d", task.view.lastProgress, task.view.lastProgressEvent, task.view.revision)
	}
}

func TestTaskViewReducerTerminalDominatesLateProgress(t *testing.T) {
	now := time.Now()
	state, changed := reduceTaskView(taskViewState{}, taskViewEvent{
		kind: taskViewProgress, at: now,
		progress: agent.ProgressEvent{Sequence: 4, State: agent.ProgressStateRunning, Text: "进展：运行测试"},
	})
	if !changed || state.lastProgressSourceSeq != 4 {
		t.Fatalf("state=%#v changed=%v", state, changed)
	}
	state, changed = reduceTaskView(state, taskViewEvent{kind: taskViewTerminal, at: now.Add(time.Second), terminalState: "completed"})
	if !changed || !state.closed || state.terminalState != "completed" {
		t.Fatalf("terminal state=%#v changed=%v", state, changed)
	}
	late, changed := reduceTaskView(state, taskViewEvent{
		kind: taskViewProgress, at: now.Add(2 * time.Second),
		progress: agent.ProgressEvent{Sequence: 5, Text: "进展：晚到事件"},
	})
	if changed || late.lastProgress != state.lastProgress {
		t.Fatalf("late progress changed terminal state: before=%#v after=%#v", state, late)
	}
}

func TestTaskViewReducerKeepsFirstTerminalEvent(t *testing.T) {
	firstAt := time.Now()
	state, changed := reduceTaskView(taskViewState{}, taskViewEvent{
		kind: taskViewTerminal, at: firstAt, terminalState: "completed",
	})
	if !changed {
		t.Fatal("first terminal event must change state")
	}
	late, changed := reduceTaskView(state, taskViewEvent{
		kind: taskViewTerminal, at: firstAt.Add(time.Second), terminalState: "failed",
	})
	if changed || late.terminalState != "completed" || !late.terminalAt.Equal(firstAt) {
		t.Fatalf("late terminal replaced first terminal: before=%#v after=%#v changed=%v", state, late, changed)
	}
}

func TestActiveTaskLocalProgressCanFollowSequencedAgentProgress(t *testing.T) {
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "codex"})
	now := time.Now()
	if _, ok := task.recordProgress(now, agent.ProgressEvent{Sequence: 20, Text: "进展：运行测试"}); !ok {
		t.Fatal("sequenced progress must be recorded")
	}
	if display, ok := task.recordLocalProgressText(now.Add(time.Second), "已发送引导对话。"); !ok || display != "已发送引导对话。" {
		t.Fatalf("local progress=(%q,%v)", display, ok)
	}
	if _, ok := task.recordProgress(now.Add(2*time.Second), agent.ProgressEvent{Sequence: 19, Text: "进展：迟到事件"}); ok {
		t.Fatal("local progress must not reset the agent sequence watermark")
	}
}

func TestRunningTasksUsesSameStructuredProgressDisplay(t *testing.T) {
	h := NewHandler(nil, nil)
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{
		owner: "user-1", agentName: "claude", message: "检查发布状态",
	})
	if !started {
		t.Fatal("task must start")
	}
	event := agent.ProgressEvent{
		ID: "tool:build", Kind: agent.ProgressKindTool, State: agent.ProgressStateRunning,
		Sequence: 3, Text: "工具：运行发布检查（进行中）",
	}
	display, ok := task.recordProgress(time.Now(), event)
	if !ok {
		t.Fatal("progress must be recorded")
	}
	status := h.handleListActiveTasks("user-1")
	if !strings.Contains(status, display) {
		t.Fatalf("/ps=%q, want display %q", status, display)
	}
}

func TestTaskViewReducerBuildsCompactStructuredTimeline(t *testing.T) {
	now := time.Now()
	state := taskViewState{}
	events := []agent.ProgressEvent{
		{ID: "plan", Kind: agent.ProgressKindPlan, State: agent.ProgressStateRunning, Sequence: 1, Text: "定位进度展示差异"},
		{ID: "plan", Kind: agent.ProgressKindPlan, State: agent.ProgressStateRunning, Sequence: 2, Text: "补充回归测试"},
		{ID: "command:test", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning, Sequence: 3, Text: "运行 go test ./messaging"},
		{ID: "command:test", Kind: agent.ProgressKindCommand, State: agent.ProgressStateCompleted, Sequence: 4, Text: "运行 go test ./messaging"},
		{ID: "file:progress", Kind: agent.ProgressKindFile, State: agent.ProgressStateRunning, Sequence: 5, Text: "修改 messaging/progress.go"},
	}
	for index, progress := range events {
		var changed bool
		state, changed = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress, at: now.Add(time.Duration(index) * time.Second), progress: progress,
		})
		if !changed {
			t.Fatalf("event %d was not reduced: %#v", index, progress)
		}
	}

	card, timeline := renderTaskProgressCard(state)
	if !timeline {
		t.Fatalf("structured events should enable timeline: %q", card)
	}
	for _, want := range []string{
		"✅ 定位进度展示差异",
		"• 补充回归测试",
		"✅ 运行 go test ./messaging",
		"• 修改 messaging/progress.go",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card=%q, want %q", card, want)
		}
	}
	if strings.Count(card, "运行 go test ./messaging") != 1 {
		t.Fatalf("command lifecycle should update one timeline entry: %q", card)
	}
	if state.lastProgress != "修改 messaging/progress.go" {
		t.Fatalf("lastProgress=%q, want latest display for /ps", state.lastProgress)
	}
}

func TestTaskViewReducerBoundsCompactTimeline(t *testing.T) {
	state := taskViewState{}
	for index := 0; index < taskProgressTimelineLimit+3; index++ {
		var changed bool
		state, changed = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress,
			at:   time.Unix(int64(index), 0),
			progress: agent.ProgressEvent{
				ID: "command-" + string(rune('a'+index)), Kind: agent.ProgressKindCommand,
				State: agent.ProgressStateCompleted, Sequence: uint64(index + 1),
				Text: "步骤 " + string(rune('A'+index)),
			},
		})
		if !changed {
			t.Fatalf("event %d was not reduced", index)
		}
	}
	if len(state.progressTimeline) != taskProgressTimelineLimit {
		t.Fatalf("timeline length=%d, want %d", len(state.progressTimeline), taskProgressTimelineLimit)
	}
	card, timeline := renderTaskProgressCard(state)
	if !timeline || strings.Contains(card, "步骤 A") || !strings.Contains(card, "步骤 K") {
		t.Fatalf("bounded card=%q timeline=%t", card, timeline)
	}
}

func TestActiveTaskProgressReanchorSnapshotUsesCompactTimeline(t *testing.T) {
	task := &activeAgentTask{progress: &progressSession{}}
	for _, event := range []agent.ProgressEvent{
		{ID: "plan", Kind: agent.ProgressKindPlan, State: agent.ProgressStateCompleted, Sequence: 1, Text: "定位问题"},
		{ID: "tool", Kind: agent.ProgressKindTool, State: agent.ProgressStateRunning, Sequence: 2, Text: "使用 CodeGraph · codegraph_explore"},
	} {
		if _, recorded := task.recordProgressUpdate(time.Now(), event); !recorded {
			t.Fatalf("event was not recorded: %#v", event)
		}
	}

	_, snapshot, ok := task.progressReanchorSnapshot()
	if !ok || !strings.Contains(snapshot, "**执行进度**") ||
		!strings.Contains(snapshot, "✅ 定位问题") ||
		!strings.Contains(snapshot, "• 使用 CodeGraph · codegraph_explore") {
		t.Fatalf("snapshot=%q ok=%t, want compact timeline", snapshot, ok)
	}
}
