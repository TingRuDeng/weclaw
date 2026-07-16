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
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	ag.watchReply, ag.watchDone = "本地任务完成", watchDone
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

func TestCodexSwitchHidesAppThreadStateReadError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{ThreadID: "thread-active"})
	ag.threadStateErr = errors.New("app-server unavailable")
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(169, "/cx cd weclaw"))
	if text := strings.Join(calls.texts(), "\n"); !strings.Contains(text, "所有权已保留") || strings.Contains(text, "app-server unavailable") {
		t.Fatalf("切换响应不得暴露状态读取失败，messages=%#v", calls.texts())
	}
}

func TestCodexSwitchHidesMissingActiveTurnError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	state := agent.CodexThreadState{ThreadID: "thread-active", Active: true, Preview: "本地 App 发起的任务"}
	h.agents["codex"] = newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(170, "/cx cd weclaw"))
	key := buildCodexConversationID("user-1", "codex", workspace)
	if _, ok := h.activeTask(key); ok {
		t.Fatal("缺少 active turn 时不应登记外部任务镜像")
	}
	if text := strings.Join(calls.texts(), "\n"); !strings.Contains(text, "所有权已保留") || strings.Contains(text, "active turn") {
		t.Fatalf("切换响应不得暴露 active turn 细节，messages=%#v", calls.texts())
	}
}

func TestCodexStopInterruptsExternalActiveTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-active", workspace, "本地任务会话", "2026-07-06T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	state := agent.CodexThreadState{
		ThreadID: "thread-active", Active: true, ActiveTurnID: "turn-active", Preview: "本地 App 发起的任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	ag.watchDone = make(chan struct{})
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

func TestCodexReservedExternalTaskRejectsGuideAndStopBeforeRuntimeCall(t *testing.T) {
	h := NewHandler(nil, nil)
	state := agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	h.defaultName = "codex"
	h.agents["codex"] = ag
	prepared, opts := testExternalCodexReservationInput(nil, nil)
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer h.cancelExternalCodexTaskReservation(reservation)
	_, err = h.ensureCodexSessions().updateControlIntent(codexControlIntentUpdate{
		ThreadID: opts.threadID, Owner: codexControlRemote,
		RouteBindingKey: "route-1\x00codex", ConversationID: opts.conversationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.storePendingGuide(opts.conversationID, pendingAgentTask{message: "补充要求", run: func() {}})
	guide, guideHandled := h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: opts.conversationID, agentName: "codex", actor: opts.actorUserID,
	})
	stop, stopHandled := h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: opts.conversationID, agent: ag, actor: opts.actorUserID,
	})
	if !guideHandled || !stopHandled || !strings.Contains(guide, "观察尚未激活") || !strings.Contains(stop, "观察尚未激活") {
		t.Fatalf("guide=(%v,%q) stop=(%v,%q)", guideHandled, guide, stopHandled, stop)
	}
	if ag.bindCalls != 0 || ag.steerThreadID != "" || ag.interruptCalls != 0 {
		t.Fatalf("reserved 阶段调用了 runtime：inspect=%d steer=%q interrupt=%d", ag.bindCalls, ag.steerThreadID, ag.interruptCalls)
	}
}

func TestCodexSelectedDesktopTaskImmediatelyAcceptsGuideAndStop(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.setActiveTarget("turn-b")
	fixture.agent.watchDone = make(chan struct{})
	fixture.h.defaultName = "codex"
	fixture.h.agents["codex"] = fixture.agent
	request := fixture.request("thread-b")
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil {
		t.Fatal(err)
	}
	defer close(fixture.agent.watchDone)
	key := result.route.conversationID
	fixture.h.storePendingGuide(key, pendingAgentTask{message: "补充要求", run: func() {}})

	guide, guideHandled := fixture.h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agentName: "codex", actor: request.actorUserID,
	})
	stop, stopHandled := fixture.h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agent: fixture.agent, actor: request.actorUserID,
	})

	if !guideHandled || !strings.Contains(guide, "已发送") ||
		!stopHandled || !strings.Contains(stop, "等待任务终态") {
		t.Fatalf("guide=(%v,%q) stop=(%v,%q)", guideHandled, guide, stopHandled, stop)
	}
	if fixture.agent.steerThreadID != "thread-b" || fixture.agent.steerTurnID != "turn-b" ||
		fixture.agent.interruptThreadID != "thread-b" || fixture.agent.interruptTurnID != "turn-b" {
		t.Fatalf("steer=(%q,%q) interrupt=(%q,%q)", fixture.agent.steerThreadID,
			fixture.agent.steerTurnID, fixture.agent.interruptThreadID, fixture.agent.interruptTurnID)
	}
	wantSteer, wantInterrupt := fixture.agent.steerThreadID, fixture.agent.interruptCalls
	fixture.h.storePendingGuide(key, pendingAgentTask{message: "越权要求", run: func() {}})
	unauthorizedGuide, _ := fixture.h.steerPendingGuideToExternalCodex(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agentName: "codex", actor: "other-actor",
	})
	unauthorizedStop, _ := fixture.h.interruptExternalCodexTask(externalCodexTaskCommand{
		ctx: context.Background(), key: key, agent: fixture.agent, actor: "other-actor",
	})
	if !strings.Contains(unauthorizedGuide, "只有任务发起人") ||
		!strings.Contains(unauthorizedStop, "只有任务发起人") {
		t.Fatalf("guide=%q stop=%q", unauthorizedGuide, unauthorizedStop)
	}
	if fixture.agent.steerThreadID != wantSteer || fixture.agent.interruptCalls != wantInterrupt {
		t.Fatal("非 owner actor 不应调用活动 Desktop turn")
	}
}
