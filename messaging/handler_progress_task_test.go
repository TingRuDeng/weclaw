package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestSendToNamedAgentNativeStreamCompletesCardAndNotifies(t *testing.T) {
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
	if len(reply.Texts) != 1 || reply.Texts[0] != "任务已完成，请查看上方卡片。" {
		t.Fatalf("texts = %#v", reply.Texts)
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
	waitForNoActiveTask(t, h, "ou_user", ag)
	if reply.Stream.Completed != "[claude] 第1条结果" {
		t.Fatalf("completed = %q", reply.Stream.Completed)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "任务已完成，请查看上方卡片。" {
		t.Fatalf("texts = %#v", reply.Texts)
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

func TestNativeStreamProgressUsesLatestCodexAppLine(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{
		fakeAgent: fakeAgent{reply: "最终结果"},
		progressDeltas: []string{
			"进展：Codex 正在分析请求。",
			"进展：Codex 正在执行命令并产生输出。",
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
	if !strings.Contains(last, "进展：Codex 已产生代码或文件变更。") {
		t.Fatalf("stream update=%q, want latest codex app progress line", last)
	}
	for _, stale := range []string{"进展：Codex 正在分析请求。", "进展：Codex 正在执行命令并产生输出。"} {
		if strings.Contains(last, stale) {
			t.Fatalf("stream update=%q should not keep stale progress %q", last, stale)
		}
	}
}

func TestFinalReplyOutsideStreamDoesNotPutOrdinaryAnswerInCard(t *testing.T) {
	finalReply := strings.Join([]string{
		"本轮未联网检索，未使用 subagent。",
		"",
		"1. 流水页切到“消费”时，顶部显示本月摘要。",
		"2. 摘要卡支持用户选择左右切换。",
		"3. 点击摘要卡进入过滤后的流水列表。",
	}, "\n")
	h := NewHandler(nil, nil)
	h.agents["mock"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: finalReply},
		progressDeltas: []string{"进展：Agent 正在整理结果。"},
		delay:          taskQueueProbeDelay,
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
		t.Fatalf("completed=%q, want status-only task card", reply.Stream.Completed)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "[mock] "+finalReply {
		t.Fatalf("texts=%#v, want ordinary final answer as text", reply.Texts)
	}
	if len(reply.Stream.Updates) == 0 {
		t.Fatal("stream should keep task status updates")
	}
	for _, update := range reply.Stream.Updates {
		if strings.Contains(update, "本轮未联网检索") || strings.Contains(update, "流水页切到") {
			t.Fatalf("stream update should not contain ordinary final answer body, updates=%#v", reply.Stream.Updates)
		}
	}
	if !strings.Contains(reply.Stream.Updates[len(reply.Stream.Updates)-1], "Agent 正在整理结果") {
		t.Fatalf("stream updates=%#v, want latest explicit status", reply.Stream.Updates)
	}
}

func TestHandlePlatformMessagePassesTextAndImageToAgent(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "input.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	ag := &fakeAgent{reply: "ok", info: agent.AgentInfo{Name: "mock", Type: "test"}}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent { return ag }, nil)
	h.SetDefaultAgent("mock", ag)
	h.SetSaveDir(dir)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "om_img_text",
		Text:      "请分析这张图",
		Attachments: []platform.Attachment{{
			Kind:     platform.AttachmentImage,
			Path:     imagePath,
			FileName: "input.png",
		}},
	}, reply)

	if !strings.Contains(ag.lastChatMessage(), "请分析这张图") ||
		!strings.Contains(ag.lastChatMessage(), "用户发送了一张图片") ||
		!strings.Contains(ag.lastChatMessage(), "本地路径：") {
		t.Fatalf("agent message=%q, want text and image path", ag.lastChatMessage())
	}
}

func TestFinishProgressWithReplyKeepsAttachmentReplyOutsideStream(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(reportPath, []byte("pdf"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	var finalText string
	consumed := finishProgressWithReply(func(text string, failed bool) bool {
		finalText = text
		return true
	}, "已生成：\n"+reportPath, false)

	if consumed {
		t.Fatal("attachment reply should not be consumed by stream")
	}
	if finalText != "" {
		t.Fatalf("finalText=%q, want generic stream finish", finalText)
	}
}

func TestApprovalHandlerWaitsForChoice(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resultCh := make(chan string, 1)
	go func() {
		optionID, err := h.approvalHandlerForUser("ou_user", "ou_user", reply)(ctx, agent.ApprovalRequest{
			ToolCall: json.RawMessage(`{"cmd":"rm file"}`),
			Options: []agent.ApprovalOption{
				{ID: "allow_once", Name: "允许", Kind: "allow"},
				{ID: "deny_once", Name: "拒绝", Kind: "deny"},
			},
		})
		if err != nil {
			resultCh <- "error:" + err.Error()
			return
		}
		resultCh <- optionID
	}()

	waitUntil(t, func() bool { return hasPendingApprovalForTest(h, "ou_user") })
	if !h.consumePendingApproval("ou_user", "allow_once") {
		t.Fatal("pending approval should consume choice")
	}

	select {
	case got := <-resultCh:
		if got != "allow_once" {
			t.Fatalf("approval result=%q, want allow_once", got)
		}
	case <-ctx.Done():
		t.Fatal("approval handler did not return")
	}
}

func TestBroadcastProgressUsesAgentPrefix(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "codex ok"},
		progressDeltas: []string{"codex delta"},
		delay:          50 * time.Millisecond,
	}
	h.agents["claude"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "claude ok"},
		progressDeltas: []string{"claude delta"},
		delay:          50 * time.Millisecond,
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.EnableTyping = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	cfg.SummaryIntervalSeconds = 0
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.broadcastToAgents(broadcastAgentsRequest{
		ctx:          context.Background(),
		platformName: platform.PlatformWeChat,
		userID:       "user-1",
		routeUserID:  "user-1",
		replyWriter:  reply,
		names:        []string{"codex", "claude"},
		message:      "hello",
	})

	if !containsText(calls.texts(), "[codex] codex delta") {
		t.Fatalf("expected codex progress prefix, messages=%#v", calls.texts())
	}
	if !containsText(calls.texts(), "[claude] claude delta") {
		t.Fatalf("expected claude progress prefix, messages=%#v", calls.texts())
	}
}

func TestBroadcastProgressUsesFeishuAccountOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	ag.fakeAgent.info = agent.AgentInfo{Name: "slow", Type: "cli", Command: "slow"}
	h.agents["slow"] = ag
	timeoutCfg := progressConfigWithTaskTimeout()
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): timeoutCfg,
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	runWithExpectedTaskTimeout(t, func(ctx context.Context) {
		h.broadcastToAgents(broadcastAgentsRequest{
			ctx:          ctx,
			platformName: platform.PlatformFeishu,
			accountID:    "cli_a",
			userID:       "ou_user",
			routeUserID:  "ou_user",
			replyWriter:  reply,
			names:        []string{"slow"},
			message:      "hello",
		})
	})
	if !containsText(reply.Texts, "本轮执行超时已被中止") {
		t.Fatalf("reply=%#v, want timeout from account progress config", reply.Texts)
	}
}
