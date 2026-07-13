package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type feishuExternalProgressFixture struct {
	h         *Handler
	workspace string
	watchDone chan struct{}
	reply     *platformtest.Replier
}

// newFeishuExternalProgressFixture 创建关闭飞书进度的外部 Codex 任务场景。
func newFeishuExternalProgressFixture(t *testing.T) feishuExternalProgressFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	offCfg := config.DefaultProgressConfig()
	offCfg.Mode = progressModeOff
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): offCfg,
	})
	watchDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-watchDone:
		default:
			close(watchDone)
		}
	})
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadState: agent.CodexThreadState{
			ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
		},
		watchReply: "本地任务完成", watchDone: watchDone,
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	return feishuExternalProgressFixture{h: h, workspace: workspace, watchDone: watchDone, reply: reply}
}

func TestCodexExternalAppTaskUsesFeishuAccountProgress(t *testing.T) {
	fixture := newFeishuExternalProgressFixture(t)
	fixture.h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "/cx cd weclaw",
	}, fixture.reply)
	close(fixture.watchDone)
	waitUntil(t, func() bool {
		_, active := fixture.h.activeTask(buildCodexConversationID("ou_user", "codex", fixture.workspace))
		return !active
	})
	if !containsText(fixture.reply.Texts, "本地任务完成") {
		t.Fatalf("texts=%#v, want final text reply", fixture.reply.Texts)
	}
	if fixture.reply.Stream.Completed != "" {
		t.Fatalf("completed=%q, want no stream completion when account progress is off", fixture.reply.Stream.Completed)
	}
}

func TestCodexSwitchShowsAppThreadStateReadError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent:      fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadStateErr: errors.New("app-server unavailable"),
	}
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(169, "/cx cd weclaw"))
	if text := strings.Join(calls.texts(), "\n"); !strings.Contains(text, "Codex App 当前任务状态读取失败: app-server unavailable") {
		t.Fatalf("切换响应应暴露状态读取失败，messages=%#v", calls.texts())
	}
}

func TestCodexSwitchShowsMissingActiveTurnError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent:   fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadState: agent.CodexThreadState{ThreadID: "thread-active", Active: true, Preview: "本地 App 发起的任务"},
	}
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(170, "/cx cd weclaw"))
	key := buildCodexConversationID("user-1", "codex", workspace)
	if _, ok := h.activeTask(key); ok {
		t.Fatal("缺少 active turn 时不应登记外部任务镜像")
	}
	if text := strings.Join(calls.texts(), "\n"); !strings.Contains(text, "未找到 active turn") {
		t.Fatalf("切换响应应提示无法接管 active thread，messages=%#v", calls.texts())
	}
}

func TestCodexStopInterruptsExternalActiveTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadState: agent.CodexThreadState{
			ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
		},
		watchDone: make(chan struct{}),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(167, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(168, "/stop"))
	if ag.interruptThreadID != "thread-active" || ag.interruptTurnID != "turn-active" {
		t.Fatalf("interrupt=(%q,%q), want active thread turn", ag.interruptThreadID, ag.interruptTurnID)
	}
	if !containsText(calls.texts(), "已发送停止请求，等待任务终态") {
		t.Fatalf("/stop should confirm interrupt and wait for terminal, messages=%#v", calls.texts())
	}
}
