package messaging

import (
	"context"
	"encoding/json"
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
		if strings.Contains(text, "实时片段") {
			t.Fatalf("summary mode should not send realtime snippet, got messages %#v", calls.texts())
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

func TestStartProgressSessionStreamModeKeepsLegacySnippet(t *testing.T) {
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

	onProgress("第一段第二段第三段")
	waitForText(t, calls, "实时片段，仅供预览")
	stop()
}

func TestSendToNamedAgentUsesAgentProgressOverride(t *testing.T) {
	h := NewHandler(nil, nil)
	h.agents["codex"] = &fakeProgressAgent{
		fakeAgent:      fakeAgent{reply: "最终结果"},
		progressDeltas: []string{"第一段第二段第三段"},
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
	h.sendToNamedAgent(context.Background(), platform.PlatformWeChat, "user-1", "user-1", reply, "codex", "hello", "client-1")

	waitForText(t, calls, "实时片段，仅供预览")
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
	h.sendToNamedAgent(context.Background(), platform.PlatformFeishu, "feishu:ou_user", "feishu:ou_user", reply, "mock", "hello", "client-1")

	if reply.Stream.Completed != "[mock] 最终结果" {
		t.Fatalf("completed=%q, want final reply in stream", reply.Stream.Completed)
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts=%#v, want final reply consumed by stream", reply.Texts)
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
	h.broadcastToAgents(context.Background(), platform.PlatformWeChat, "user-1", "user-1", reply, []string{"codex", "claude"}, "hello")

	if !containsText(calls.texts(), "[codex] 实时片段，仅供预览") {
		t.Fatalf("expected codex progress prefix, messages=%#v", calls.texts())
	}
	if !containsText(calls.texts(), "[claude] 实时片段，仅供预览") {
		t.Fatalf("expected claude progress prefix, messages=%#v", calls.texts())
	}
}
