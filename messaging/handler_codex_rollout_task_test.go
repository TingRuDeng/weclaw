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

type rolloutMirrorFixture struct {
	h              *Handler
	agent          *fakeCodexLiveAgent
	rolloutPath    string
	turnID         string
	conversationID string
	reply          *platformtest.Replier
	sessionKey     string
}

// newRolloutMirrorFixture 创建只读镜像本地 Codex rollout 的飞书场景。
func newRolloutMirrorFixture(t *testing.T) rolloutMirrorFixture {
	t.Helper()
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
	ag := newFakeCodexLiveAgent(
		agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: threadID},
	)
	h.agents["codex"] = ag
	progressOff := config.DefaultProgressConfig()
	progressOff.Mode = progressModeOff
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): progressOff,
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant:dm:chat:user"
	return rolloutMirrorFixture{
		h: h, agent: ag, rolloutPath: rolloutPath, turnID: turnID,
		conversationID: buildCodexConversationID(sessionKey, "codex", workspace),
		reply:          reply, sessionKey: sessionKey,
	}
}

// switchAndAssertRolloutMirror 验证切换反馈和外部任务镜像登记。
func switchAndAssertRolloutMirror(t *testing.T, fixture rolloutMirrorFixture) {
	t.Helper()
	fixture.h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "ou_user",
		Text:      "/cx switch thread-rollout-active",
		Metadata:  map[string]string{feishuSessionMetadataKey: fixture.sessionKey},
	}, fixture.reply)
	if _, ok := fixture.h.activeTask(fixture.conversationID); !ok {
		t.Fatalf("切换到本地运行中 rollout 后应登记外部任务镜像，texts=%#v", fixture.reply.TextsSnapshot())
	}
	notice := strings.Join(fixture.reply.TextsSnapshot(), "\n")
	for _, want := range []string{"共享 Codex 任务正在进行", "修复跨进程任务反馈", "正在核对任务状态"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("switch notice=%q, want %q", notice, want)
		}
	}
	if strings.Contains(notice, "/guide") || strings.Contains(notice, "/stop") {
		t.Fatalf("switch notice=%q, rollout mirror must not advertise unavailable commands", notice)
	}
	if strings.Contains(notice, "需在 Codex App 中操作") ||
		!strings.Contains(notice, "新消息会直接发送到当前任务") ||
		!strings.Contains(notice, "结果会自动返回当前会话") {
		t.Fatalf("switch notice=%q, must describe direct input and automatic result delivery", notice)
	}
}

// steerAndStopRolloutMirror 验证输入立即进入当前任务，但无法从远端停止只读镜像。
func steerAndStopRolloutMirror(t *testing.T, fixture rolloutMirrorFixture) {
	t.Helper()
	fixture.h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "补充要求",
		Metadata: map[string]string{feishuSessionMetadataKey: fixture.sessionKey},
	}, fixture.reply)
	task, _ := fixture.h.activeTask(fixture.conversationID)
	texts := fixture.reply.TextsSnapshot()
	if task.pendingGuide() != "" || fixture.agent.steerThreadID != "thread-rollout-active" ||
		fixture.agent.steerTurnID != fixture.turnID || fixture.agent.steerMessage != "补充要求" ||
		!containsText(texts, "已发送到当前共享 Codex 任务") || containsText(texts, queuedAgentMessage) {
		t.Fatalf("pending=%q steer=(%q,%q,%q) texts=%#v", task.pendingGuide(), fixture.agent.steerThreadID,
			fixture.agent.steerTurnID, fixture.agent.steerMessage, texts)
	}
	fixture.h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "/stop",
		Metadata: map[string]string{feishuSessionMetadataKey: fixture.sessionKey},
	}, fixture.reply)
	texts = fixture.reply.TextsSnapshot()
	if _, ok := fixture.h.activeTask(fixture.conversationID); !ok || !containsText(texts, "暂不支持从飞书或微信停止") {
		t.Fatalf("texts=%#v, /stop must not cancel read-only rollout mirror", texts)
	}
}

// completeAndAssertRolloutMirror 验证本地完成后回传结果，且不会另开 turn。
func completeAndAssertRolloutMirror(t *testing.T, fixture rolloutMirrorFixture) {
	t.Helper()
	appendCodexRolloutRecord(t, fixture.rolloutPath, rolloutProgressRecord("正在整理最终结果"))
	appendCodexRolloutRecord(t, fixture.rolloutPath, rolloutTaskCompleteRecord(fixture.turnID, "本地任务最终结果"))
	waitUntil(t, func() bool {
		return containsText(fixture.reply.TextsSnapshot(), "本地任务最终结果")
	})
	if fixture.agent.chatCallCount() != 0 {
		t.Fatalf("chat calls=%d, steer 输入不应在完成后自动续跑", fixture.agent.chatCallCount())
	}
	texts := fixture.reply.TextsSnapshot()
	if containsText(texts, "回复“确认”执行该消息") {
		t.Fatalf("texts=%#v, rollout task 自动续跑不应要求确认", texts)
	}
}

func TestFeishuSwitchMirrorsRunningCodexAppRollout(t *testing.T) {
	fixture := newRolloutMirrorFixture(t)
	switchAndAssertRolloutMirror(t, fixture)
	steerAndStopRolloutMirror(t, fixture)
	completeAndAssertRolloutMirror(t, fixture)
}

func TestRolloutGuideCreatesRelayCardWithoutSuccessText(t *testing.T) {
	fixture := newLiveGuideRelayFixture(t, true)
	if !fixture.h.storePendingGuide(fixture.route.conversationID, pendingAgentTask{message: "补充 rollout 要求", run: func() {}}) {
		t.Fatal("failed to store pending guide")
	}
	reply := newGuideRelayTestReplier("card-rollout-guide")
	text, handled := fixture.h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: fixture.route.conversationID,
		agentName: "codex", actor: fixture.opts.userID,
		reply: reply, messageKey: "feishu\x00cli_a\x00rollout-guide-1",
	})
	if !handled || text != "" {
		t.Fatalf("handled=%v text=%q", handled, text)
	}
	if fixture.task.pendingGuide() != "" || reply.openAttempts != 1 || fixture.oldReply.stream.supersededCount() != 1 {
		t.Fatalf("pending=%q open=%d superseded=%d", fixture.task.pendingGuide(), reply.openAttempts, fixture.oldReply.stream.supersededCount())
	}
	if calls := fixture.agent.guideSnapshot(); len(calls) != 1 || calls[0].message != "补充 rollout 要求" {
		t.Fatalf("steer calls=%#v", calls)
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
