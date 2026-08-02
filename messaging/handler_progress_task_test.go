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

	if reply.Stream.Completed != "" {
		t.Fatalf("completed=%q, want status-only completion card", reply.Stream.Completed)
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

	if !strings.Contains(reply.Stream.Failed, "boom") {
		t.Fatalf("failed card=%q，want 即时任务卡显示真实失败", reply.Stream.Failed)
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

func TestNativeStreamCollapsesStructuredTimelineAtTerminal(t *testing.T) {
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
		latest: "运行回归测试", card: timeline, timeline: true,
	})
	time.Sleep(taskQueueProbeDelay)
	_ = finish(progressStatusOnlyComplete, false)

	if len(reply.Stream.Updates) == 0 || reply.Stream.Updates[len(reply.Stream.Updates)-1] != timeline {
		t.Fatalf("updates=%#v, want complete compact timeline", reply.Stream.Updates)
	}
	if reply.Stream.Completed != "" {
		t.Fatalf("completed=%q, want progress collapsed at status-only terminal", reply.Stream.Completed)
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
	if !strings.Contains(last, "**执行进度**") || !strings.Contains(last, "定位问题") ||
		!strings.Contains(last, "✅ 运行 go test ./messaging") {
		t.Fatalf("last update=%q, want compact structured timeline", last)
	}
	if reply.Stream.Completed != "[mock] 最终结果" {
		t.Fatalf("completed=%q, want final result without progress timeline", reply.Stream.Completed)
	}
}
