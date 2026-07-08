package messaging

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestDuplicateTextFallbackWhenMessageIDZero(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	msg := newTextMessage(0, "同一个任务")

	handleTestWeChatMessage(h, context.Background(), client, msg)
	handleTestWeChatMessage(h, context.Background(), client, msg)

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("MessageID=0 duplicate text should only start agent once, chatCalls=%d", ag.chatCallCount())
	}
}

func TestDuplicateMessageIDStillDeduped(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	msg := newTextMessage(99, "同一个任务")

	handleTestWeChatMessage(h, context.Background(), client, msg)
	handleTestWeChatMessage(h, context.Background(), client, msg)

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("same MessageID should only start agent once, chatCalls=%d", ag.chatCallCount())
	}
}

func TestHandleMessage_AbsolutePathTextGoesToDefaultAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	text := "/Volumes/Data/code/MyCode/cc-switch/codex-switch.sh看下具体实现"

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(100, text))

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("absolute path text should call default agent once, chatCalls=%d", ag.chatCallCount())
	}
	if ag.lastChatMessage() != text {
		t.Fatalf("agent message=%q, want original text", ag.lastChatMessage())
	}
	if containsText(calls.texts(), "Usage: specify one agent") {
		t.Fatalf("absolute path text should not reply usage, messages=%#v", calls.texts())
	}
}

func TestHandleMessageRemovedSwitchCommandGoesToDefaultAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "ok"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(101, "/sw reload"))

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("已删除的 /sw 命令应落到默认 Agent，chatCalls=%d", ag.chatCallCount())
	}
	if ag.lastChatMessage() != "/sw reload" {
		t.Fatalf("agent message=%q，期望保留原始已删除命令文本", ag.lastChatMessage())
	}
	if containsText(calls.texts(), "不再支持从微信端切换 Codex 账号") {
		t.Fatalf("已删除的 /sw 命令不应再被内置命令消费，messages=%#v", calls.texts())
	}
}

func TestHandleMessage_FileMessageSavesFileAndSendsPathToAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	saveDir := t.TempDir()
	h.SetSaveDir(saveDir)
	ag := &fakeAgent{reply: "已分析"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.cdnDownloader = func(_ context.Context, queryParam string, aesKey string) ([]byte, error) {
		if queryParam != "download-param" || aesKey != "aes-key" {
			t.Fatalf("download args=(%q,%q), want download-param/aes-key", queryParam, aesKey)
		}
		return []byte("文件内容"), nil
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)
	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newFileMessage(10, "方案.txt"))

	waitForFakeAgentCalls(t, ag, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("file message should start agent once, chatCalls=%d", ag.chatCallCount())
	}
	if !strings.Contains(ag.lastChatMessage(), "用户发送了一个文件") {
		t.Fatalf("agent message should describe incoming file, got %q", ag.lastChatMessage())
	}
	if !strings.Contains(ag.lastChatMessage(), "方案.txt") {
		t.Fatalf("agent message should include file name, got %q", ag.lastChatMessage())
	}
	if !strings.Contains(ag.lastChatMessage(), saveDir) {
		t.Fatalf("agent message should include saved local path, got %q", ag.lastChatMessage())
	}
	if _, err := os.Stat(extractSavedPathFromAgentMessage(ag.lastChatMessage())); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
}

func TestHandleMessage_FileMessageWithoutMediaDoesNotCallAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{reply: "不应调用"}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	msg := newFileMessage(11, "broken.txt")
	msg.ItemList[0].FileItem.Media = nil

	handleTestWeChatMessage(h, context.Background(), client, msg)

	if ag.chatCallCount() != 0 {
		t.Fatalf("file without media should not call agent, chatCalls=%d", ag.chatCallCount())
	}
	if !containsText(calls.texts(), "文件保存失败") {
		t.Fatalf("expected file failure reply, got %#v", calls.texts())
	}
}

func extractSavedPathFromAgentMessage(message string) string {
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "本地路径：") {
			return strings.TrimPrefix(line, "本地路径：")
		}
	}
	return ""
}

func TestSendReplyWithMediaUsesChunksForLongFinalText(t *testing.T) {
	h := NewHandler(nil, nil)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	reply := strings.Join([]string{
		strings.Repeat("甲", 12),
		strings.Repeat("乙", 12),
		strings.Repeat("丙", 12),
	}, "\n")

	replyWriter := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendReplyWithMedia(withTextReplyChunkLimit(context.Background(), 15), replyWriter, "user-1", "codex", reply)

	texts := calls.texts()
	if len(texts) != 3 {
		t.Fatalf("sent texts=%#v, want three chunks", texts)
	}
	wantReply := wechat.FormatTextForWeChatDisplay(reply)
	if strings.Join(texts, "\n") != wantReply {
		t.Fatalf("joined chunks=%q, want WeChat display reply %q", strings.Join(texts, "\n"), wantReply)
	}
}

func TestSendReplyWithMediaKeepsChoiceLikeFinalReplyAsText(t *testing.T) {
	h := NewHandler(nil, nil)
	replyWriter := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.sendReplyWithMedia(context.Background(), replyWriter, "user-1", "codex", "请选择一个方案：\n1. 继续\n2. 暂停")

	if len(replyWriter.Texts) != 1 || replyWriter.Texts[0] != "请选择一个方案：\n1. 继续\n2. 暂停" {
		t.Fatalf("texts=%#v, want original final reply", replyWriter.Texts)
	}
	if len(replyWriter.Choices) != 0 {
		t.Fatalf("choices=%#v, want no auto choice card", replyWriter.Choices)
	}
}

func TestRawCommandStopCancelsActiveCodexTask(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"},
		},
	}
	h.agents["codex"] = ag
	key := h.agentExecutionKey("feishu:ou_user", "codex", ag)
	task, taskCtx, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{owner: "feishu:ou_user", agentName: "codex", message: "hi"})
	if !started {
		t.Fatal("beginActiveTask started=false, want true")
	}
	replyWriter := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "feishu:ou_user",
		MessageID: "card-stop-1",
		RawCommand: &platform.CardAction{
			Action: "stop",
		},
	}, replyWriter)

	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("task context was not canceled")
	}
	if task.shouldSendFinal() {
		t.Fatal("task should not send final after stop")
	}
	h.finishActiveTask(key, task)
}
