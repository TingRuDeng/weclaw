package messaging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuSwitchMirrorsRunningCodexAppRollout(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	threadID := "thread-rollout-active"
	turnID := "turn-rollout-active"
	writeLocalCodexSession(t, codexDir, threadID, workspace, "本地任务会话", "2026-07-10T11:21:38Z")
	rolloutPath := localRolloutPathForTest(codexDir, threadID)
	appendCodexRolloutRecord(t, rolloutPath, rolloutTaskStartedRecord(turnID))
	appendCodexRolloutRecord(t, rolloutPath, rolloutUserMessageRecord(turnID, "修复跨进程任务反馈"))
	appendCodexRolloutRecord(t, rolloutPath, rolloutProgressRecord("正在核对任务状态"))
	h.SetCodexLocalSessionDir(codexDir)
	h.SetAllowedWorkspaceRoots([]string{workspace})
	h.defaultName = "codex"
	ag := &fakeCodexThreadAgent{
		fakeAgent:   fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadState: agent.CodexThreadState{ThreadID: threadID},
	}
	h.agents["codex"] = ag
	progressOff := config.DefaultProgressConfig()
	progressOff.Mode = progressModeOff
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): progressOff,
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant:dm:chat:user"

	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "ou_user",
		Text:      "/cx switch " + threadID,
		Metadata:  map[string]string{feishuSessionMetadataKey: sessionKey},
	}, reply)

	conversationID := buildCodexConversationID(sessionKey, "codex", workspace)
	if _, ok := h.activeTask(conversationID); !ok {
		t.Fatal("切换到本地运行中 rollout 后应登记外部任务镜像")
	}
	notice := strings.Join(reply.Texts, "\n")
	for _, want := range []string{"Codex App 任务正在进行", "修复跨进程任务反馈", "正在核对任务状态"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("switch notice=%q, want %q", notice, want)
		}
	}
	if strings.Contains(notice, "/guide") || strings.Contains(notice, "/stop") {
		t.Fatalf("switch notice=%q, read-only rollout mirror must not advertise remote control", notice)
	}
	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "补充要求",
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, reply)
	task, _ := h.activeTask(conversationID)
	if task.pendingGuide() != "补充要求" || !containsText(reply.Texts, "自动执行此消息") {
		t.Fatalf("pending=%q texts=%#v, read-only task must queue the next message", task.pendingGuide(), reply.Texts)
	}
	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "/stop",
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, reply)
	if _, ok := h.activeTask(conversationID); !ok || !containsText(reply.Texts, "请在 Codex App 中停止") {
		t.Fatalf("texts=%#v, /stop must not cancel read-only rollout mirror", reply.Texts)
	}

	appendCodexRolloutRecord(t, rolloutPath, rolloutProgressRecord("正在整理最终结果"))
	appendCodexRolloutRecord(t, rolloutPath, rolloutTaskCompleteRecord(turnID, "本地任务最终结果"))
	waitUntil(t, func() bool {
		return ag.lastChatMessage() == "补充要求"
	})
	if !containsText(reply.Texts, "本地任务最终结果") {
		t.Fatalf("texts=%#v, rollout task 完成后应返回最终结果", reply.Texts)
	}
	if containsText(reply.Texts, "回复“确认”执行该消息") {
		t.Fatalf("texts=%#v, rollout task 自动续跑不应要求确认", reply.Texts)
	}
}

func localRolloutPathForTest(codexDir string, threadID string) string {
	return filepath.Join(codexDir, "sessions", "2026", "04", "29", "rollout-"+threadID+".jsonl")
}

func appendCodexRolloutRecord(t *testing.T, path string, record map[string]any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open rollout: %v", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(record); err != nil {
		t.Fatalf("append rollout: %v", err)
	}
}

func rolloutTaskStartedRecord(turnID string) map[string]any {
	return map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": turnID}}
}

func rolloutUserMessageRecord(turnID string, text string) map[string]any {
	return map[string]any{"type": "response_item", "payload": map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": text}},
		"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": turnID},
	}}
}

func rolloutProgressRecord(text string) map[string]any {
	return map[string]any{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": text, "phase": "commentary"}}
}

func rolloutTaskCompleteRecord(turnID string, text string) map[string]any {
	return map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "task_complete", "turn_id": turnID, "last_agent_message": text,
	}}
}
