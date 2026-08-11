package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestStartProgressSessionSummaryModeDoesNotSendRealtimeSnippet(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeSummary
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "")
	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "修复实时回复碎片化", cfg)

	onProgress("这里是一段 Codex 正文 delta")
	waitForText(t, calls, "处理中，请耐心等待")
	stop()

	for _, text := range calls.texts() {
		if strings.Contains(text, "这里是一段 Codex 正文 delta") {
			t.Fatalf("summary mode should not send raw delta, got messages %#v", calls.texts())
		}
		if strings.Contains(text, "实时状态") {
			t.Fatalf("summary mode should not send realtime status, got messages %#v", calls.texts())
		}
	}
}

func TestStartProgressSessionDefaultTypingModeDoesNotSendTextFeedback(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	cfg := config.DefaultProgressConfig()
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "")
	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "查询当前工作目录", cfg)

	onProgress("正在生成结果")
	time.Sleep(taskQueueProbeDelay)
	stop()

	if texts := calls.texts(); len(texts) != 0 {
		t.Fatalf("default typing mode should not send progress text, got %#v", texts)
	}
	if typings := calls.typings(); len(typings) == 0 {
		t.Fatal("default typing mode should still send typing status")
	}
}

func TestStartProgressSessionStreamModeSendsLastStatusLine(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "")
	onProgress, stop := h.startProgressSession(context.Background(), reply, "", "修复实时回复碎片化", cfg)

	onProgress("第一段\n第二段\n第三段")
	waitForText(t, calls, "第三段")
	stop()

	if containsText(calls.texts(), "第一段") {
		t.Fatalf("stream progress should not send old lines, messages=%#v", calls.texts())
	}
	if containsText(calls.texts(), "实时状态") {
		t.Fatalf("stream progress should not wrap latest line, messages=%#v", calls.texts())
	}
}

func TestSendToNamedAgentUsesAgentProgressOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "最终结果"},
		progressDeltas: []string{"第一段\n第二段\n第三段"},
		delay:          50 * time.Millisecond,
	}
	globalCfg := config.DefaultProgressConfig()
	globalCfg.EnableTyping = boolPtr(false)
	globalCfg.InitialDelaySeconds = 0
	globalCfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(globalCfg)
	streamCfg := config.ProgressConfig{Mode: progressModeStream}
	h.SetAgentProgressConfigs(map[string]config.ProgressConfig{"codex": streamCfg})

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformWeChat, userID: "user-1", routeUserID: "user-1", reply: reply, name: "codex", message: "hello", clientID: "client-1"})

	waitForText(t, calls, "第三段")
	if containsText(calls.texts(), "实时状态") {
		t.Fatalf("stream progress should not wrap latest line, messages=%#v", calls.texts())
	}
}

func TestSendToNamedAgentNativeStreamConsumesFinalReply(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "最终结果"},
		progressDeltas: []string{"过程片段"},
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "feishu:ou_user", routeUserID: "feishu:ou_user", reply: reply, name: "mock", message: "hello", clientID: "client-1"})

	if reply.Stream.Completed != "[mock] 最终结果" {
		t.Fatalf("completed=%q, want final reply in stream", reply.Stream.Completed)
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts=%#v, want final reply consumed by stream", reply.Texts)
	}
}

func TestSendToNamedAgentNativeStreamCompletesCardWithoutSuccessNotice(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{fakeAgent: fakeAgent{reply: "最终结果"}}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	h.SetProgressConfig(cfg)

	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, Streaming: true, StreamCompletionNotification: true,
	})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "feishu:ou_user", routeUserID: "feishu:ou_user", reply: reply, name: "mock", message: "hello", clientID: "client-1"})

	if reply.Stream.Completed != "[mock] 最终结果" {
		t.Fatalf("completed = %q", reply.Stream.Completed)
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts = %#v, want final result only in task card", reply.Texts)
	}
}

func TestClaudeTaskOpensNativeStreamBeforeAgentReturns(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}
	h.agents["claude"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	h.SetProgressConfig(cfg)
	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, Streaming: true, StreamCompletionNotification: true,
	})
	done := make(chan struct{})
	go func() {
		h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "ou_user", routeUserID: "ou_user", reply: reply, name: "claude", message: "hello", clientID: "client-1"})

		close(done)
	}()

	select {
	case <-reply.StreamOpened:
	case <-time.After(taskWaitTimeout):
		t.Fatal("Claude 返回前未创建任务卡")
	}
	waitForAgentEnter(t, ag)
	close(ag.release)
	select {
	case <-done:
	case <-time.After(taskWaitTimeout):
		t.Fatal("Claude 任务未结束")
	}
	waitForNoActiveTask(t, noActiveTaskExpectation{handler: h, routeUserID: "ou_user", agent: ag})
	if reply.Stream.Completed != "[claude] 第1条结果" {
		t.Fatalf("completed = %q", reply.Stream.Completed)
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts = %#v, want final result only in task card", reply.Texts)
	}
}

func TestSendToNamedAgentNativeStreamCanKeepFinalReplyOutsideStream(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "最终结果"},
		progressDeltas: []string{"过程片段"},
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{string(platform.PlatformFeishu): cfg})

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true, FinalReplyOutsideStream: true})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "feishu:ou_user", routeUserID: "feishu:ou_user", reply: reply, name: "mock", message: "hello", clientID: "client-1"})

	if reply.Stream.Completed != "[mock] 过程片段" {
		t.Fatalf("completed=%q, want terminal card to retain the Agent progress reply", reply.Stream.Completed)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "[mock] 最终结果" {
		t.Fatalf("texts=%#v, want final reply as separate message", reply.Texts)
	}
}

func TestFinalReplyOutsideStreamFailureDoesNotExposeStatusSentinel(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{fakeAgent: fakeAgent{err: errors.New("boom")}}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	h.SetProgressConfig(cfg)
	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, Streaming: true, FinalReplyOutsideStream: true,
	})

	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "ou_user", routeUserID: "ou_user", reply: reply, name: "mock", message: "hello", clientID: "client-1"})

	if reply.Stream.Failed != "" {
		t.Fatalf("failed card=%q，want compact status-only terminal", reply.Stream.Failed)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "boom") {
		t.Fatalf("texts=%#v，want 单条真实失败回复", reply.Texts)
	}
}

func TestNativeStreamProgressCollapsesRepeatedStructuredStatus(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{
		fakeAgent: fakeAgent{reply: "最终结果"},
		progressDeltas: []string{
			"进展：Codex 已产生代码或文件变更。",
			"进展：Codex 已产生代码或文件变更。",
			"进展：Codex 已产生代码或文件变更。",
		},
		delay: taskQueueProbeDelay,
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{string(platform.PlatformFeishu): cfg})

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true, FinalReplyOutsideStream: true})
	h.sendToNamedAgent(agentMessageRequest{ctx: context.Background(), platformName: platform.PlatformFeishu, userID: "feishu:ou_user", routeUserID: "feishu:ou_user", reply: reply, name: "mock", message: "hello", clientID: "client-1"})

	if len(reply.Stream.Updates) == 0 {
		t.Fatal("stream should receive progress updates")
	}
	last := reply.Stream.Updates[len(reply.Stream.Updates)-1]
	if strings.Count(last, "进展：") != 1 {
		t.Fatalf("stream update should contain one latest status, updates=%#v", reply.Stream.Updates)
	}
}

func TestNativeStreamShowsStructuredTimelineAfterAgentReply(t *testing.T) {
	h := NewHandler(nil, nil)
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "修复进度卡", cfg,
	)
	timeline := "**执行进度**\n\n- ✅ 定位问题\n- • 运行回归测试"
	session.onTaskProgress(taskProgressUpdate{
		latest: "运行回归测试", card: timeline, timeline: true, commentary: true,
	})
	time.Sleep(taskQueueProbeDelay)
	_ = finish(progressStatusOnlyComplete, false)

	wantActive := timeline + "\n\n" + platform.TaskStreamThinkingIndicator
	if len(reply.Stream.Updates) == 0 || reply.Stream.Updates[len(reply.Stream.Updates)-1] != wantActive {
		t.Fatalf("updates=%#v, want compact timeline followed by thinking", reply.Stream.Updates)
	}
	if reply.Stream.Completed != "" {
		t.Fatalf("completed=%q, want progress collapsed at status-only terminal", reply.Stream.Completed)
	}
}

func TestNativeStreamShowsFirstEffectiveNonCommandProgress(t *testing.T) {
	h := NewHandler(nil, nil)
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "检查任务", cfg,
	)
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "codex"})
	fileUpdate, ok := task.recordProgressUpdate(time.Now(), agent.ProgressEvent{
		ID: "file:progress", Kind: agent.ProgressKindFile,
		State: agent.ProgressStateCompleted, Sequence: 1, Text: "检查进度实现",
	})
	if !ok {
		t.Fatal("file progress should be recorded")
	}
	session.onTaskProgress(fileUpdate)
	waitUntil(t, func() bool { return len(reply.Stream.UpdatesSnapshot()) > 0 })
	updates := reply.Stream.UpdatesSnapshot()
	first := updates[len(updates)-1]
	if !strings.Contains(first, "检查进度实现") || !strings.HasSuffix(strings.TrimSpace(first), platform.TaskStreamThinkingIndicator) {
		t.Fatalf("first update=%q, want file progress followed by thinking", first)
	}
	if strings.Contains(first, "等待 Agent") || strings.Contains(first, "连接正常") {
		t.Fatalf("first update=%q, redundant synthetic waiting copy must be absent", first)
	}

	const commentary = "我先检查当前实现，再运行回归测试。"
	commentaryUpdate, ok := task.recordProgressUpdate(time.Now().Add(time.Second), agent.ProgressEvent{
		ID: "agent-message:first", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 2, Text: commentary,
	})
	if !ok {
		t.Fatal("commentary should be recorded")
	}
	session.onTaskProgress(commentaryUpdate)
	waitUntil(t, func() bool { return len(reply.Stream.UpdatesSnapshot()) > 1 })
	updates = reply.Stream.UpdatesSnapshot()
	last := updates[len(updates)-1]
	if !strings.Contains(last, "检查进度实现") || !strings.Contains(last, commentary) {
		t.Fatalf("update=%q, want accumulated structured progress and Agent reply", last)
	}
	if strings.Count(last, "思考中.....") != 1 || !strings.HasSuffix(strings.TrimSpace(last), "思考中.....") {
		t.Fatalf("update=%q, want one thinking indicator at the bottom", last)
	}
	if strings.Contains(last, "等待 Agent") || strings.Contains(last, "连接正常") {
		t.Fatalf("update=%q, redundant synthetic waiting copy must be absent", last)
	}
	_ = finish(progressStatusOnlyComplete, false)
}

func TestTaskProgressUpdateHasEffectiveProgressAcceptsNonCommandEvents(t *testing.T) {
	tests := []struct {
		name   string
		update taskProgressUpdate
		want   bool
	}{
		{name: "commentary flag", update: taskProgressUpdate{commentary: true}, want: true},
		{name: "current explanation", update: taskProgressUpdate{currentExplanation: "正在检查实现"}, want: true},
		{name: "commentary event", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindCommentary, Text: "正在检查实现"}}}, want: true},
		{name: "plan", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindPlan, Text: "先定位问题"}}}, want: true},
		{name: "file", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindFile, Text: "修改进度实现"}}}, want: true},
		{name: "tool", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindTool, Text: "读取项目结构"}}}, want: true},
		{name: "command", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindCommand, Text: "执行 go test"}}}, want: false},
		{name: "thought", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindThought, Text: "内部推理"}}}, want: false},
		{name: "status", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindStatus, Text: "等待 Agent"}}}, want: false},
		{name: "approval", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindApproval, Text: "等待审批"}}}, want: false},
		{name: "empty plan", update: taskProgressUpdate{timelineItems: []agent.ProgressEvent{{Kind: agent.ProgressKindPlan}}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskProgressUpdateHasEffectiveProgress(tt.update); got != tt.want {
				t.Fatalf("taskProgressUpdateHasEffectiveProgress()=%t, want %t", got, tt.want)
			}
		})
	}
}

func TestNativeStreamKeepsUpdatingAfterCommentaryLeavesBoundedTimeline(t *testing.T) {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	_, finish, session := NewHandler(nil, nil).startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "检查任务", cfg,
	)
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "codex"})
	task.setProgressTimelineLimit(1)
	commentaryUpdate, _ := task.recordProgressUpdate(time.Now(), agent.ProgressEvent{
		ID: "agent-message:first", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 1, Text: "我先检查当前实现。",
	})
	session.onTaskProgress(commentaryUpdate)
	waitUntil(t, func() bool { return len(reply.Stream.UpdatesSnapshot()) == 1 })

	fileUpdate, _ := task.recordProgressUpdate(time.Now().Add(time.Second), agent.ProgressEvent{
		ID: "file:progress", Kind: agent.ProgressKindFile,
		State: agent.ProgressStateCompleted, Sequence: 2, Text: "已完成实现检查",
	})
	if len(fileUpdate.timelineItems) != 1 || fileUpdate.timelineItems[0].Kind != agent.ProgressKindFile {
		t.Fatalf("timeline=%#v, want commentary evicted by the positive window", fileUpdate.timelineItems)
	}
	session.onTaskProgress(fileUpdate)
	waitUntil(t, func() bool { return len(reply.Stream.UpdatesSnapshot()) == 2 })
	last := reply.Stream.UpdatesSnapshot()[1]
	if !strings.Contains(last, "已完成实现检查") || !strings.HasSuffix(last, platform.TaskStreamThinkingIndicator) {
		t.Fatalf("last update=%q, want continued progress after the first Agent reply", last)
	}
	_ = finish(progressStatusOnlyComplete, false)
}

func TestCodexCommentaryProgressUpdatesTimelineCard(t *testing.T) {
	h := NewHandler(nil, nil)
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "[codex] ", "codex", "/workspace/project", "原生进度", cfg,
	)
	const native = "我先检查当前实现。\n\n- 保留 Codex 原文\n- 不生成工具时间线"
	task, _ := newActiveAgentTask(context.Background(), activeTaskMeta{owner: "user-1", agentName: "codex"})
	update, ok := task.recordProgressUpdate(time.Now(), agent.ProgressEvent{
		ID: "agent-message:message-1", Kind: agent.ProgressKindCommentary,
		State: agent.ProgressStateCompleted, Sequence: 1, Text: native,
	})
	if !ok {
		t.Fatal("native commentary should create a task progress update")
	}
	session.onTaskProgress(update)
	time.Sleep(taskQueueProbeDelay)
	_ = finish("最终结果", false)

	if len(reply.Stream.Updates) == 0 || !strings.Contains(reply.Stream.Updates[len(reply.Stream.Updates)-1], "**执行进度**") ||
		strings.Contains(reply.Stream.Updates[len(reply.Stream.Updates)-1], "**当前说明**") ||
		!strings.Contains(reply.Stream.Updates[len(reply.Stream.Updates)-1], native) {
		t.Fatalf("updates=%#v", reply.Stream.Updates)
	}
	if reply.Stream.Completed != "最终结果" {
		t.Fatalf("completed=%q", reply.Stream.Completed)
	}
}

func TestCodexNativeCommentaryEntersTimelineButFinalStaysOutsideCard(t *testing.T) {
	const native = "我先检查当前实现。\n\n- 保留 Codex 原文\n- 不生成工具时间线"
	h := NewHandler(nil, nil)
	ag := &fakeStructuredProgressAgent{
		fakeProgressAgent: fakeProgressAgent{
			fakeAgent: fakeAgent{reply: "最终结果"}, delay: taskQueueProbeDelay,
		},
		events: []agent.ProgressEvent{{
			ID: "agent-message:message-1", Kind: agent.ProgressKindCommentary,
			State: agent.ProgressStateCompleted, Sequence: 1, Text: native,
		}},
	}
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{string(platform.PlatformFeishu): cfg})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true, FinalReplyOutsideStream: true})
	executionKey := h.agentExecutionKeyForRoute("feishu:ou_user", "feishu:ou_user", "codex", ag)

	h.sendToNamedAgent(agentMessageRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "feishu:ou_user", routeUserID: "feishu:ou_user", reply: reply,
		name: "codex", message: "检查进度", clientID: "client-1",
	})
	waitUntil(t, func() bool {
		_, active := h.activeTask(executionKey)
		return !active
	})

	foundCommentary := false
	for _, update := range reply.Stream.Updates {
		if strings.Contains(update, "**执行进度**") && !strings.Contains(update, "**当前说明**") && strings.Contains(update, native) {
			foundCommentary = true
		}
		if strings.Contains(update, "最终结果") {
			t.Fatalf("final answer entered task card: updates=%#v", reply.Stream.Updates)
		}
	}
	if !foundCommentary {
		t.Fatalf("commentary missing from task card: updates=%#v", reply.Stream.Updates)
	}
	wantTerminal := "[codex] **执行进度**\n\n" + native
	if reply.Stream.Completed != wantTerminal || len(reply.Texts) != 1 || reply.Texts[0] != "[codex] 最终结果" {
		t.Fatalf("completed=%q texts=%#v, want independent final result", reply.Stream.Completed, reply.Texts)
	}
}

func TestStructuredAgentNativeStreamBuildsCompactTimeline(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeStructuredProgressAgent{
		fakeProgressAgent: fakeProgressAgent{
			fakeAgent: fakeAgent{reply: "最终结果"}, delay: taskQueueProbeDelay,
		},
		events: []agent.ProgressEvent{
			{ID: "plan", Kind: agent.ProgressKindPlan, State: agent.ProgressStateRunning, Sequence: 1, Text: "定位问题"},
			{ID: "command:test", Kind: agent.ProgressKindCommand, State: agent.ProgressStateRunning, Sequence: 2, Text: "运行 go test ./messaging"},
			{ID: "command:test", Kind: agent.ProgressKindCommand, State: agent.ProgressStateCompleted, Sequence: 3, Text: "运行 go test ./messaging"},
			{ID: "agent-message:first", Kind: agent.ProgressKindCommentary, State: agent.ProgressStateCompleted, Sequence: 4, Text: "我正在整理验证结果。"},
		},
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{string(platform.PlatformFeishu): cfg})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})

	h.sendToNamedAgent(agentMessageRequest{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "feishu:ou_user", routeUserID: "feishu:ou_user", reply: reply,
		name: "mock", message: "检查进度", clientID: "client-1",
	})

	if len(reply.Stream.Updates) == 0 {
		t.Fatal("structured task should update the native stream")
	}
	last := reply.Stream.Updates[len(reply.Stream.Updates)-1]
	if !strings.HasPrefix(last, "[mock] ") || !strings.Contains(last, "**执行进度**") || !strings.Contains(last, "定位问题") ||
		!strings.Contains(last, "✅ 运行 go test ./messaging") {
		t.Fatalf("last update=%q, want compact structured timeline", last)
	}
	if reply.Stream.Completed != "[mock] 最终结果" {
		t.Fatalf("completed=%q, want final result without progress timeline", reply.Stream.Completed)
	}
}
