package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestReserveExternalCodexTaskRejectsDifferentTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	conversationID := "conversation-1"
	existing, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user-1", agentName: "codex", codexThreadID: "thread-1", codexTurnID: "turn-old",
	})
	if !started {
		t.Fatal("未能建立旧观察任务")
	}
	defer h.finishActiveTask(conversationID, existing)
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-new",
		}, Controllable: true},
	}
	_, err := h.reserveExternalCodexTask(externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: conversationID, threadID: "thread-1",
	}, prepared)
	if !errors.Is(err, errExternalCodexTaskReservationConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestCancelExternalCodexTaskReservationRemovesUnstartedTask(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
		watch: func(context.Context, func(string)) (string, error) { return "完成", nil },
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: "conversation-1", threadID: "thread-1",
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	h.cancelExternalCodexTaskReservation(reservation)
	h.cancelExternalCodexTaskReservation(reservation)
	if _, active := h.activeTask(opts.conversationID); active {
		t.Fatal("取消预留后不应残留 active task")
	}
}

func TestReserveExternalCodexTaskReusesSameThreadTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: "conversation-1", threadID: "thread-1",
	}
	first, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer h.cancelExternalCodexTaskReservation(first)
	second, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !second.reused || second.task != first.task {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	h.cancelExternalCodexTaskReservation(second)
	if task, active := h.activeTask(opts.conversationID); !active || task != first.task {
		t.Fatal("取消复用预留不应清理原观察任务")
	}
}

func TestCancelExternalCodexTaskReservationKeepsActivatedTask(t *testing.T) {
	h := NewHandler(nil, nil)
	watchStarted := make(chan struct{})
	watchDone := make(chan struct{})
	defer func() {
		select {
		case <-watchDone:
		default:
			close(watchDone)
		}
	}()
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
		watch: func(context.Context, func(string)) (string, error) {
			close(watchStarted)
			<-watchDone
			return "完成", nil
		},
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: "conversation-1", threadID: "thread-1",
		reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	h.activateExternalCodexTaskReservation(reservation)
	h.activateExternalCodexTaskReservation(reservation)
	waitUntil(t, func() bool { return channelClosed(watchStarted) })
	h.cancelExternalCodexTaskReservation(reservation)
	h.cancelExternalCodexTaskReservation(reservation)
	if task, active := h.activeTask(opts.conversationID); !active || task != reservation.task {
		t.Fatal("已激活的观察任务不应被预留取消逻辑清理")
	}
	close(watchDone)
	waitUntil(t, func() bool {
		_, active := h.activeTask(opts.conversationID)
		return !active
	})
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadState: agent.CodexThreadState{
			ThreadID:             "thread-active",
			Active:               true,
			ActiveTurnID:         "turn-active",
			WaitingOnUserInput:   true,
			Preview:              "本地 App 发起的任务",
			LastAgentMessageText: "",
		},
		watchDone: make(chan struct{}),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(160, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(161, "/cx switch 0"))

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
	if !strings.Contains(text, "Codex App 任务正在进行") || !strings.Contains(text, "本地 App 发起的任务") {
		t.Fatalf("switch reply should show active task, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "/guide") || !strings.Contains(text, "/stop") || !strings.Contains(text, "/cancel") {
		t.Fatalf("active switch reply should show all task controls, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "模型: gpt-5.5") || !strings.Contains(text, "推理强度: high") {
		t.Fatalf("active switch reply should keep session model status, messages=%#v", calls.texts())
	}
}

func TestCodexGuideSteersExternalActiveTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			reply: "不应该新开 turn",
		},
		threadState: agent.CodexThreadState{
			ThreadID:     "thread-active",
			Active:       true,
			ActiveTurnID: "turn-active",
			Preview:      "本地 App 发起的任务",
		},
		watchDone: make(chan struct{}),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(162, "/cx cd weclaw"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(163, "/cx switch 0"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(164, "补充要求"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(165, "/guide"))

	if ag.steerThreadID != "thread-active" || ag.steerTurnID != "turn-active" || ag.steerMessage != "补充要求" {
		t.Fatalf("steer=(%q,%q,%q), want active thread turn message", ag.steerThreadID, ag.steerTurnID, ag.steerMessage)
	}
	if ag.chatCallCount() != 0 {
		t.Fatalf("/guide for external active turn should not start new chat, calls=%d", ag.chatCallCount())
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, queuedAgentMessage) {
		t.Fatalf("普通消息应发送简洁排队提示，messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "已发送到当前 Codex App 任务") {
		t.Fatalf("/guide should confirm steer, messages=%#v", calls.texts())
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
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadState: agent.CodexThreadState{
			ThreadID:     "thread-active",
			Active:       true,
			ActiveTurnID: "turn-active",
			Preview:      "本地 App 发起的任务",
		},
		watchReply: "本地任务完成",
		watchDone:  watchDone,
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(166, "/cx cd weclaw"))
	close(watchDone)

	waitForText(t, calls, "本地任务完成")
}
