package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexSwitchActiveAppThreadRegistersExternalTask(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	appendLocalCodexTurnContext(t, codexDir, "thread-active", "gpt-5.5", "high")
	h.SetCodexLocalSessionDir(codexDir)
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active",
		WaitingOnUserInput: true, Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.watchDone = make(chan struct{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(160, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(161, "/cx switch 1"))

	key := buildCodexConversationID("user-1", "codex", workspace)
	task, ok := h.activeTask(key)
	if !ok {
		t.Fatal("切换 active Codex App thread 后应登记外部任务镜像")
	}
	task.mu.Lock()
	external := task.isExternalCodexLocked()
	threadID := task.codexThreadID
	turnID := task.codexTurnID
	task.mu.Unlock()
	if !external || threadID != "thread-active" || turnID != "turn-active" {
		t.Fatalf("external task=(%v,%q,%q), want active thread/turn", external, threadID, turnID)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "共享 Codex 任务正在进行") || !strings.Contains(text, "本地 App 发起的任务") {
		t.Fatalf("switch reply should show active task, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "新消息会直接发送到当前任务") || !strings.Contains(text, "/stop") ||
		strings.Contains(text, "/guide") || strings.Contains(text, "/cancel") {
		t.Fatalf("active switch reply should show immediate input and stop controls, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "模型: gpt-5.5") || !strings.Contains(text, "推理强度: high") {
		t.Fatalf("active switch reply should keep session model status, messages=%#v", calls.texts())
	}
}

func TestCodexMessageSteersExternalActiveTurnImmediately(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true,
		ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.reply, ag.watchDone = "不应该新开 turn", make(chan struct{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(162, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(163, "/cx switch 1"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(164, "补充要求"))

	if ag.steerThreadID != "thread-active" || ag.steerTurnID != "turn-active" || ag.steerMessage != "补充要求" {
		t.Fatalf("steer=(%q,%q,%q), want active thread turn message", ag.steerThreadID, ag.steerTurnID, ag.steerMessage)
	}
	if ag.chatCallCount() != 0 {
		t.Fatalf("active turn follow-up should not start new chat, calls=%d", ag.chatCallCount())
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已发送到当前共享 Codex 任务") {
		t.Fatalf("message should confirm immediate steer, messages=%#v", calls.texts())
	}
	if strings.Contains(text, queuedAgentMessage) {
		t.Fatalf("message must not enter a private WeClaw queue, messages=%#v", calls.texts())
	}
}

func TestCodexExternalAppTaskSendsFinalReply(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	watchDone := make(chan struct{})
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true,
		ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	ag.watchReply, ag.watchDone = "本地任务完成", watchDone
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(166, "/cx cd weclaw"))
	close(watchDone)

	waitForText(t, calls, "本地任务完成")
}
