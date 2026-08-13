package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
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

func TestClaudeAgentMessageRendersAsCurrentExplanationWithoutReplacingTimeline(t *testing.T) {
	now := time.Now()
	state, changed := reduceTaskView(taskViewState{}, taskViewEvent{
		kind: taskViewProgress, at: now,
		progress: agent.ProgressEvent{ID: "command:test", Kind: agent.ProgressKindCommand, Sequence: 1, Text: "运行测试"},
	})
	if !changed {
		t.Fatal("command progress was not recorded")
	}
	const native = "我先检查当前实现。\n\n接下来运行回归测试。"
	state, changed = reduceTaskView(state, taskViewEvent{
		kind: taskViewProgress, at: now.Add(time.Second),
		progress: agent.ProgressEvent{ID: "agent-message:message-1", Kind: agent.ProgressKindMessage, Sequence: 2, Text: native},
	})
	if !changed {
		t.Fatal("native message sequence was not observed")
	}
	card, timeline := renderTaskProgressCard(state)
	if !timeline || !strings.Contains(card, "**执行进度**") || !strings.Contains(card, "运行测试") ||
		!strings.Contains(card, "**当前说明**") || !strings.Contains(card, native) {
		t.Fatalf("card=%q timeline=%t, want timeline and independent current explanation", card, timeline)
	}
	if len(state.progressTimeline) != 1 || state.progressTimeline[0].ID != "command:test" {
		t.Fatalf("timeline=%#v, native message must not enter structured progress", state.progressTimeline)
	}
	if state.lastProgress != "运行测试" || state.lastProgressEvent.ID != "command:test" {
		t.Fatalf("last progress=%q event=%#v, want latest structured progress", state.lastProgress, state.lastProgressEvent)
	}
}

func TestClaudeAgentMessageDoesNotConsumeStructuredTimelineLimit(t *testing.T) {
	limit := 2
	state := taskViewState{progressTimelineLimit: &limit}
	events := []agent.ProgressEvent{
		{ID: "command:first", Kind: agent.ProgressKindCommand, Sequence: 1, Text: "第一条结构化进度"},
		{ID: "agent-message:message-1", Kind: agent.ProgressKindMessage, Sequence: 2, Text: "中间说明"},
		{ID: "command:second", Kind: agent.ProgressKindCommand, Sequence: 3, Text: "第二条结构化进度"},
	}
	for index, progress := range events {
		var changed bool
		state, changed = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress, at: time.Unix(int64(index), 0), progress: progress,
		})
		if !changed {
			t.Fatalf("event %d was not reduced: %#v", index, progress)
		}
	}
	if len(state.progressTimeline) != 2 ||
		state.progressTimeline[0].ID != "command:first" ||
		state.progressTimeline[1].ID != "command:second" {
		t.Fatalf("timeline=%#v, want two structured entries", state.progressTimeline)
	}
	card, timeline := renderTaskProgressCard(state)
	if !timeline || !strings.Contains(card, "第一条结构化进度") ||
		!strings.Contains(card, "第二条结构化进度") || !strings.Contains(card, "**当前说明**") ||
		!strings.Contains(card, "中间说明") {
		t.Fatalf("card=%q timeline=%t", card, timeline)
	}
}

func TestClaudeAgentMessageAloneBuildsCurrentExplanationSnapshot(t *testing.T) {
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "claude"})
	const commentary = "我先检查当前实现，再运行回归测试。"
	update, ok := task.recordProgressUpdate(time.Now(), agent.ProgressEvent{
		ID: "agent-message:message-1", Kind: agent.ProgressKindMessage,
		State: agent.ProgressStateCompleted, Sequence: 1, Text: commentary,
	})
	if !ok {
		t.Fatal("commentary should produce a stream card snapshot")
	}
	if update.timeline || !strings.Contains(update.card, "**当前说明**") || !strings.Contains(update.card, commentary) {
		t.Fatalf("update=%#v", update)
	}
	if len(update.timelineItems) != 0 {
		t.Fatalf("timeline=%#v, commentary must not enter structured timeline", update.timelineItems)
	}

	task.recordTerminalView(time.Now().Add(time.Second), "completed")
	task.mu.Lock()
	terminalCard, terminalTimeline := renderTaskProgressCard(task.view)
	task.mu.Unlock()
	if terminalTimeline || !strings.Contains(terminalCard, commentary) {
		t.Fatalf("terminal card=%q timeline=%t, want preserved current explanation", terminalCard, terminalTimeline)
	}
}

func TestClaudeCurrentExplanationUsesSingleProgressRuneLimit(t *testing.T) {
	state, changed := reduceTaskView(taskViewState{}, taskViewEvent{
		kind: taskViewProgress, at: time.Now(),
		progress: agent.ProgressEvent{
			ID: "agent-message:message-1", Kind: agent.ProgressKindMessage,
			Sequence: 1, Text: strings.Repeat("进", taskProgressTimelineItemMaxRunes+20),
		},
	})
	if !changed {
		t.Fatal("commentary was not reduced")
	}
	card, timeline := renderTaskProgressCard(state)
	if timeline || !strings.HasSuffix(card, "…") || strings.Count(card, "进") != taskProgressTimelineItemMaxRunes {
		t.Fatalf("card=%q timeline=%t", card, timeline)
	}
}

func TestCodexCommentaryAccumulatesFromFirstToLastWithoutTruncation(t *testing.T) {
	first := strings.Repeat("第一段用户可见说明。", taskProgressTimelineItemMaxRunes/8+8)
	second := "第二段用户可见说明。\n\n- 保留 Markdown 列表\n- 保留完整正文"
	events := []agent.ProgressEvent{
		{ID: "agent-message:first", Kind: agent.ProgressKindCommentary, State: agent.ProgressStateCompleted, Sequence: 1, Text: first},
		{ID: "command:test", Kind: agent.ProgressKindCommand, State: agent.ProgressStateCompleted, Sequence: 2, Text: "运行回归测试"},
		{ID: "agent-message:second", Kind: agent.ProgressKindCommentary, State: agent.ProgressStateCompleted, Sequence: 3, Text: second},
	}
	state := taskViewState{}
	for index, progress := range events {
		var changed bool
		state, changed = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress, at: time.Unix(int64(index), 0), progress: progress,
		})
		if !changed {
			t.Fatalf("event %d was not reduced: %#v", index, progress)
		}
	}

	card, timeline := renderTaskProgressCard(state)
	if !timeline || len(state.progressTimeline) != len(events) {
		t.Fatalf("timeline=%t entries=%#v", timeline, state.progressTimeline)
	}
	firstIndex := strings.Index(card, first)
	commandIndex := strings.Index(card, "运行回归测试")
	secondIndex := strings.Index(card, second)
	if firstIndex < 0 || commandIndex <= firstIndex || secondIndex <= commandIndex {
		t.Fatalf("card=%q, want complete commentary and command in source order", card)
	}
	if strings.Contains(card, "**当前说明**") {
		t.Fatalf("card=%q, Codex commentary must accumulate in the timeline", card)
	}
}

func TestTaskViewReducerDefaultsToUnlimitedCodexCommentaryTimeline(t *testing.T) {
	state := taskViewState{}
	const messageCount = 11
	for index := 0; index < messageCount; index++ {
		var changed bool
		state, changed = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress,
			at:   time.Unix(int64(index), 0),
			progress: agent.ProgressEvent{
				ID: "agent-message:" + string(rune('a'+index)), Kind: agent.ProgressKindCommentary,
				State: agent.ProgressStateCompleted, Sequence: uint64(index + 1), Text: "说明 " + string(rune('A'+index)),
			},
		})
		if !changed {
			t.Fatalf("event %d was not reduced", index)
		}
	}
	if got := len(state.progressTimeline); got != messageCount {
		t.Fatalf("timeline length=%d, want %d", got, messageCount)
	}
	card, timeline := renderTaskProgressCard(state)
	if !timeline || !strings.Contains(card, "说明 A") || !strings.Contains(card, "说明 K") {
		t.Fatalf("card=%q timeline=%t", card, timeline)
	}
}

func TestConfiguredPositiveTimelineLimitIncludesCodexCommentary(t *testing.T) {
	limit := 2
	state := taskViewState{progressTimelineLimit: &limit}
	events := []agent.ProgressEvent{
		{ID: "agent-message:first", Kind: agent.ProgressKindCommentary, Sequence: 1, Text: "第一段说明"},
		{ID: "command:test", Kind: agent.ProgressKindCommand, Sequence: 2, Text: "运行测试"},
		{ID: "agent-message:last", Kind: agent.ProgressKindCommentary, Sequence: 3, Text: "最后一段说明"},
	}
	for index, progress := range events {
		state, _ = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress, at: time.Unix(int64(index), 0), progress: progress,
		})
	}
	if got := len(state.progressTimeline); got != limit || state.progressTimeline[0].ID != "command:test" ||
		state.progressTimeline[1].ID != "agent-message:last" {
		t.Fatalf("timeline=%#v, want latest %d entries", state.progressTimeline, limit)
	}
}

func TestCodexCommentaryWithSameIDUpdatesInPlace(t *testing.T) {
	state := taskViewState{}
	for index, text := range []string{"第一版说明", "更新后的完整说明"} {
		state, _ = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress, at: time.Unix(int64(index), 0),
			progress: agent.ProgressEvent{
				ID: "agent-message:stable", Kind: agent.ProgressKindCommentary,
				State: agent.ProgressStateCompleted, Sequence: uint64(index + 1), Text: text,
			},
		})
	}
	card, timeline := renderTaskProgressCard(state)
	if !timeline || len(state.progressTimeline) != 1 || strings.Contains(card, "第一版说明") ||
		!strings.Contains(card, "更新后的完整说明") {
		t.Fatalf("card=%q timeline=%t entries=%#v", card, timeline, state.progressTimeline)
	}
}

func TestTaskViewReducerBoundsCompactTimeline(t *testing.T) {
	limit := 8
	state := taskViewState{progressTimelineLimit: &limit}
	for index := 0; index < limit+3; index++ {
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
	if len(state.progressTimeline) != limit {
		t.Fatalf("timeline length=%d, want %d", len(state.progressTimeline), limit)
	}
	card, timeline := renderTaskProgressCard(state)
	if !timeline || strings.Contains(card, "步骤 A") || !strings.Contains(card, "步骤 K") {
		t.Fatalf("bounded card=%q timeline=%t", card, timeline)
	}
}

func TestTaskViewReducerKeepsUnlimitedTimelineWhenConfiguredZero(t *testing.T) {
	limit := 0
	state := taskViewState{progressTimelineLimit: &limit}
	const eventCount = 11
	for index := 0; index < eventCount; index++ {
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
	if got, want := len(state.progressTimeline), eventCount; got != want {
		t.Fatalf("timeline length=%d, want %d", got, want)
	}
}

func TestTaskViewReducerUsesConfiguredPositiveTimelineLimit(t *testing.T) {
	limit := 3
	state := taskViewState{progressTimelineLimit: &limit}
	for index := 0; index < 5; index++ {
		state, _ = reduceTaskView(state, taskViewEvent{
			kind: taskViewProgress,
			at:   time.Unix(int64(index), 0),
			progress: agent.ProgressEvent{
				ID: "file-" + string(rune('a'+index)), Kind: agent.ProgressKindFile,
				State: agent.ProgressStateCompleted, Sequence: uint64(index + 1), Text: "文件步骤",
			},
		})
	}
	if got := len(state.progressTimeline); got != limit {
		t.Fatalf("timeline length=%d, want %d", got, limit)
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
	if !ok || !strings.Contains(snapshot.text, "**执行进度**") ||
		!strings.Contains(snapshot.text, "✅ 定位问题") ||
		!strings.Contains(snapshot.text, "• 使用 CodeGraph · codegraph_explore") {
		t.Fatalf("snapshot=%q ok=%t, want compact timeline", snapshot.text, ok)
	}
}

func TestProgressReanchorSnapshotCarriesStructuredSummaryAndDetails(t *testing.T) {
	task := &activeAgentTask{progress: &progressSession{}}
	for _, event := range []agent.ProgressEvent{
		{ID: "commentary", Kind: agent.ProgressKindCommentary, Sequence: 1, Text: "已开始处理。"},
		{ID: "tool", Kind: agent.ProgressKindTool, Sequence: 2, Text: "读取项目结构"},
	} {
		if _, recorded := task.recordProgressUpdate(time.Now(), event); !recorded {
			t.Fatalf("event was not recorded: %#v", event)
		}
	}
	if _, recorded := task.recordLocalProgressText(time.Now(), "已接收新的补充输入。"); !recorded {
		t.Fatal("local guide was not recorded")
	}
	progress, snapshot, ok := task.progressReanchorSnapshot()
	if !ok || progress == nil {
		t.Fatal("expected reanchorable progress session")
	}
	if snapshot.summary != "已接收新的补充输入。" {
		t.Fatalf("summary=%q", snapshot.summary)
	}
	if !snapshot.structured || len(snapshot.timelineItems) == 0 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if !strings.Contains(snapshot.text, "已接收新的补充输入。") || !strings.Contains(snapshot.text, "读取项目结构") {
		t.Fatalf("details=%q", snapshot.text)
	}
}

func TestProgressPresentationPreviewsLatestFiveStructuredItems(t *testing.T) {
	session := &progressSession{}
	events := []agent.ProgressEvent{
		{ID: "step-1", Kind: agent.ProgressKindCommentary, Sequence: 1, Text: "第一条进度"},
		{ID: "step-2", Kind: agent.ProgressKindCommentary, Sequence: 2, Text: "第二条进度"},
		{ID: "step-3", Kind: agent.ProgressKindCommentary, Sequence: 3, Text: "第三条进度"},
		{ID: "step-4", Kind: agent.ProgressKindCommentary, Sequence: 4, Text: "第四条进度"},
		{ID: "step-5", Kind: agent.ProgressKindCommentary, Sequence: 5, Text: "第五条进度"},
		{ID: "step-6", Kind: agent.ProgressKindCommentary, Sequence: 6, Text: "第六条进度"},
		{ID: "step-7", Kind: agent.ProgressKindCommentary, Sequence: 7, Text: "第七条进度"},
	}
	snapshot := progressCardSnapshot{
		text:          "第七条进度",
		structured:    true,
		timelineItems: events,
	}

	presentation := session.snapshotPresentationLocked(snapshot)

	for _, want := range []string{"第一条进度", "第二条进度", "第七条进度"} {
		if !strings.Contains(presentation.Details, want) {
			t.Fatalf("details=%q, want %q", presentation.Details, want)
		}
	}
	for _, hidden := range []string{"第一条进度", "第二条进度"} {
		if strings.Contains(presentation.Preview, hidden) {
			t.Fatalf("preview=%q, must omit %q", presentation.Preview, hidden)
		}
	}
	for _, want := range []string{"第三条进度", "第四条进度", "第五条进度", "第六条进度", "第七条进度"} {
		if !strings.Contains(presentation.Preview, want) {
			t.Fatalf("preview=%q, want %q", presentation.Preview, want)
		}
	}
	if !strings.HasSuffix(presentation.Preview, platform.TaskStreamThinkingIndicator) {
		t.Fatalf("preview=%q, want active indicator at bottom", presentation.Preview)
	}
}
