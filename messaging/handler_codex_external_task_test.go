package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type fakeCodexProgressSnapshotAgent struct {
	*fakeCodexLiveAgent
	progress []agent.ProgressEvent
}

func (a *fakeCodexProgressSnapshotAgent) ReadCodexThreadProgressSnapshot(
	ctx context.Context, conversationID string, threadID string,
) (agent.CodexThreadState, []agent.ProgressEvent, error) {
	state, err := a.ReadCodexThreadState(ctx, conversationID, threadID)
	return state, append([]agent.ProgressEvent(nil), a.progress...), err
}

func TestCodexSwitchActiveTurnOpensCardWithLatestFiveSnapshotEntries(t *testing.T) {
	h := NewHandler(nil, nil)
	watchDone := make(chan struct{})
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地任务",
	})
	base.watchDone = watchDone
	ag := &fakeCodexProgressSnapshotAgent{fakeCodexLiveAgent: base}
	for index := 1; index <= 6; index++ {
		ag.progress = append(ag.progress, agent.ProgressEvent{
			ID: "agent-message:" + string(rune('0'+index)), Kind: agent.ProgressKindCommentary,
			State: agent.ProgressStateCompleted, Sequence: uint64(index),
			Text: "第" + string(rune('0'+index)) + "条说明",
		})
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "user-1",
		agentName: "codex", agent: ag, conversationID: "conversation-1",
		threadID: "thread-active", workspaceRoot: "/workspace/project", progressCfg: cfg, reply: reply,
	}
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !h.activateExternalCodexTaskReservation(reservation) {
		t.Fatal("activate external task")
	}
	t.Cleanup(func() {
		close(watchDone)
		waitUntil(t, func() bool {
			_, active := h.activeTask(opts.conversationID)
			return !active
		})
	})
	select {
	case <-reply.StreamOpened:
	case <-time.After(time.Second):
		t.Fatal("progress card was not opened")
	}
	presentation := reply.Stream.Options.InitialPresentation
	if presentation == nil {
		t.Fatal("initial presentation=nil, want active-turn snapshot")
	}
	if strings.Contains(presentation.Preview, "第1条说明") || !strings.Contains(presentation.Preview, "第2条说明") ||
		!strings.Contains(presentation.Preview, "第6条说明") {
		t.Fatalf("preview=%q, want latest five snapshot entries", presentation.Preview)
	}
	if !strings.Contains(presentation.Details, "第1条说明") || !strings.Contains(presentation.Details, "第6条说明") {
		t.Fatalf("details=%q, want complete snapshot", presentation.Details)
	}
}

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
