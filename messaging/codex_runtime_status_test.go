package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexStatusReturnsSharedHostState(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	result, handled := h.dispatchCodexUtilityCommand(runtime)
	if !handled || result.ShowCard {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
	for _, want := range []string{
		"Codex 状态",
		"工作空间: " + filepath.Base(runtime.workspaceRoot),
		"会话: 未命名会话",
		"绑定: 已绑定",
		"任务: 空闲",
		"运行通道: 可用",
		"进度同步: 正常",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
	for _, obsolete := range []string{"窗口绑定:", "写入服务:", "运行模式:", "窗口角色:", "说明:"} {
		if strings.Contains(result.Reply, obsolete) {
			t.Fatalf("reply=%q, should omit %q", result.Reply, obsolete)
		}
	}
	if ag.bindCalls != 1 {
		t.Fatalf("探测次数=%d，期望 1", ag.bindCalls)
	}
}

func TestCodexStatusShowsExactTurnAndControlChannel(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		WaitingOnApproval: true, Preview: "正在等待审批",
	})
	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"thread: thread-1",
		"turn: turn-1",
		"Host: WeClaw 官方 daemon（共享）",
		"控制通道: 共享 daemon；writer 未记录，具体客户端未暴露；WeClaw 可 steer/interrupt",
		"控制意图（仅表示窗口 binding）: owner=remote route=wechat:route-1/codex",
		"控制入口（本次命令）: platform=wechat；chat/reply_to 不可追踪；未确认控制授权，只能观察当前 turn",
		"等待: 审批",
		"当前窗口 binding: 已选择该 thread",
		"进度同步: 正常",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
}

func TestCodexStatusIdentifiesExternalWriterWithoutClientID(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	conversationID := runtime.codexRoute("thread-1").conversationID
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "codex-app", routeUserID: runtime.routeUserID, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	if !started {
		t.Fatal("external Codex task should be registered")
	}
	t.Cleanup(func() {
		task.cancel()
		h.finishActiveTask(conversationID, task)
	})

	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"原始入口: Codex App 或其他前端",
		"Turn writer: 外部前端（共享 daemon；具体 client 未暴露）",
		"控制通道: 共享 daemon；外部前端 writer，具体客户端未暴露；WeClaw 可 steer/interrupt",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
}

func TestCodexStatusIdentifiesWeClawWriterProcess(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	conversationID := runtime.codexRoute("thread-1").conversationID
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "weclaw", routeUserID: runtime.routeUserID, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
		inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("WeClaw Codex task should be registered")
	}
	t.Cleanup(func() {
		task.cancel()
		h.finishActiveTask(conversationID, task)
	})

	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"原始入口: WeClaw 消息/CLI",
		"Turn writer: WeClaw 当前进程",
		"控制通道: WeClaw 当前进程，可 steer/interrupt",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
}

func TestCodexStatusUsesRuntimeBeforeStaleLocalTaskMetadata(t *testing.T) {
	got := codexStatusTurnWriterLine(agent.CodexRuntimeDesktop, codexStatusTaskInfo{
		inProcess: true,
		writerDeliveryRoute: platform.DeliveryRoute{
			Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-a",
		},
	}, true)
	if !strings.Contains(got, "Turn writer: Codex App Desktop Host") {
		t.Fatalf("writer line=%q, stale local task metadata must not override Desktop runtime", got)
	}
}

func TestCodexStatusShowsDesktopControlChannel(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.mu.Lock()
	ag.binding.Runtime = agent.CodexRuntimeDesktop
	ag.mu.Unlock()
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})

	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"Turn writer: Codex App Desktop Host（具体 client 未由 Desktop IPC 暴露）",
		"控制通道: Codex App Desktop IPC；WeClaw 可 steer，/stop 需要在 Codex App 中执行",
		"控制意图（仅表示窗口 binding）: owner=remote route=wechat:route-1/codex",
		"控制入口（本次命令）: platform=wechat；chat/reply_to 不可追踪；未确认控制授权，只能观察当前 turn",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
}

func TestCodexStatusDesktopStopMustRunInApp(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.mu.Lock()
	ag.binding.Runtime = agent.CodexRuntimeDesktop
	ag.mu.Unlock()
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})

	result := h.renderCodexStatus(runtime)
	if !strings.Contains(result.Reply, "控制通道: Codex App Desktop IPC；WeClaw 可 steer，/stop 需要在 Codex App 中执行") {
		t.Fatalf("reply=%q, Desktop status must explain that /stop stays in Codex App", result.Reply)
	}
	if strings.Contains(result.Reply, "Codex App Desktop IPC；WeClaw 可 steer/interrupt") {
		t.Fatalf("reply=%q, Desktop status must not claim WeClaw can interrupt", result.Reply)
	}
}

func TestCodexStatusMarksUnauthorizedCommandRouteAsObserver(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	conversationID := runtime.codexRoute("thread-1").conversationID
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "different-user", routeUserID: runtime.routeUserID, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
		inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("WeClaw Codex task should be registered")
	}
	t.Cleanup(func() {
		task.cancel()
		h.finishActiveTask(conversationID, task)
	})

	result := h.renderCodexStatus(runtime)
	want := "控制入口（本次命令）: platform=wechat；chat/reply_to 不可追踪；未授权，只能观察当前 turn"
	if !strings.Contains(result.Reply, want) {
		t.Fatalf("reply=%q, want unauthorized command route %q", result.Reply, want)
	}
}

func TestCodexStatusListsKnownWeClawWriterRouteAsControllable(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	writerRoute := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-writer", ChatID: "chat-writer", ReplyToID: "msg-writer",
	}
	conversationID := runtime.codexRoute("thread-1").conversationID
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: runtime.actorUserID, routeUserID: runtime.routeUserID, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
		inProcessCodexLifecycle: true, writerPlatform: writerRoute.Platform, writerAccountID: writerRoute.AccountID,
		writerDeliveryRoute: writerRoute,
	})
	if !started {
		t.Fatal("WeClaw Codex task should be registered")
	}
	t.Cleanup(func() {
		task.cancel()
		h.finishActiveTask(conversationID, task)
	})

	result := h.renderCodexStatus(runtime)
	want := "- platform=feishu account=app-writer chat=chat-writer reply_to=msg-writer；WeClaw writer，可发送普通输入、/guide、/stop"
	if !strings.Contains(result.Reply, want) {
		t.Fatalf("reply=%q, want known writer route %q", result.Reply, want)
	}
}

func TestCodexStatusListsAuthorizedCurrentCommandRoute(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	currentRoute := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-current", ChatID: "chat-current", ReplyToID: "msg-current",
	}
	runtime.req.Platform = currentRoute.Platform
	runtime.req.AccountID = currentRoute.AccountID
	runtime.req.Reply = &codexFollowerRouteReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}), route: currentRoute,
	}
	conversationID := runtime.codexRoute("thread-1").conversationID
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: runtime.actorUserID, routeUserID: runtime.routeUserID, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
		inProcessCodexLifecycle: true,
	})
	if !started {
		t.Fatal("WeClaw Codex task should be registered")
	}
	t.Cleanup(func() {
		task.cancel()
		h.finishActiveTask(conversationID, task)
	})

	result := h.renderCodexStatus(runtime)
	want := "- platform=feishu account=app-current chat=chat-current reply_to=msg-current；当前命令，可发送普通输入、/guide、/stop"
	if !strings.Contains(result.Reply, want) {
		t.Fatalf("reply=%q, want authorized current command route %q", result.Reply, want)
	}
}

func TestCodexStatusShowsExactWeClawWriterRoute(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	writerRoute := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-writer", ChatID: "chat-writer", ReplyToID: "msg-writer",
	}
	conversationID := runtime.codexRoute("thread-1").conversationID
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: runtime.routeUserID, routeUserID: runtime.routeUserID, agentName: "codex",
		runtimeOwner: agent.CodexRuntimeWeClaw, codexThreadID: "thread-1", codexTurnID: "turn-1",
		inProcessCodexLifecycle: true, writerPlatform: writerRoute.Platform, writerAccountID: writerRoute.AccountID,
		writerDeliveryRoute: writerRoute,
	})
	if !started {
		t.Fatal("WeClaw Codex task should be registered")
	}
	t.Cleanup(func() {
		task.cancel()
		h.finishActiveTask(conversationID, task)
	})

	result := h.renderCodexStatus(runtime)
	want := "Turn writer: WeClaw 当前进程；入口 platform=feishu account=app-writer chat=chat-writer reply_to=msg-writer"
	if !strings.Contains(result.Reply, want) {
		t.Fatalf("reply=%q, want exact writer route %q", result.Reply, want)
	}
}

func TestCodexStatusShowsCurrentMessageAndTurnObserverRoutes(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	currentRoute := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-current", ReplyToID: "msg-status",
	}
	runtime.req.Platform = platform.PlatformFeishu
	runtime.req.AccountID = currentRoute.AccountID
	runtime.req.Reply = &codexFollowerRouteReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}), route: currentRoute,
	}
	addCodexStatusFollower(t, h, runtime, "route-1", platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-a", ReplyToID: "msg-a",
	}, "turn-1")

	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"消息通道（本次命令）: platform=feishu account=app-a chat=chat-current reply_to=msg-status",
		"Turn 观察通道:",
		"- platform=feishu account=app-a chat=chat-a reply_to=msg-a；跟踪当前 turn",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
}

func TestCodexStatusShowsControllableFeishuChannel(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	addCodexStatusFollower(t, h, runtime, "route-1", platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-a", ReplyToID: "msg-a",
	}, "turn-1")

	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"可控制通道:",
		"- platform=feishu account=app-a chat=chat-a reply_to=msg-a；可发送普通输入、/guide、/stop",
		"Turn 观察通道:",
		"- platform=feishu account=app-a chat=chat-a reply_to=msg-a；跟踪当前 turn；同时可控制",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
}

func TestCodexStatusMarksObserverForDifferentTurnAsReadOnly(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	addCodexStatusFollower(t, h, runtime, "route-observer", platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-old", ReplyToID: "msg-old",
	}, "turn-old")

	result := h.renderCodexStatus(runtime)
	want := "- platform=feishu account=app-a chat=chat-old reply_to=msg-old；最近跟踪 turn=turn-old（当前 turn 游标未确认）；仅观察"
	if !strings.Contains(result.Reply, want) {
		t.Fatalf("reply=%q, want read-only observer route %q", result.Reply, want)
	}
	if strings.Contains(result.Reply, "chat=chat-old reply_to=msg-old；可发送") {
		t.Fatalf("reply=%q, observer for another turn must not be controllable", result.Reply)
	}
}

func TestCodexStatusShowsAllFeishuObserversForSameThread(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{
		ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
	})
	runtime.req.Platform = platform.PlatformFeishu
	runtime.req.AccountID = "app-a"
	runtime.req.Reply = &codexFollowerRouteReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		route:   platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-current"},
	}
	addCodexStatusFollower(t, h, runtime, "route-1", platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-a", ReplyToID: "msg-a",
	}, "turn-1")
	addCodexStatusFollower(t, h, runtime, "route-2", platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-b", ChatID: "chat-b", ReplyToID: "msg-b",
	}, "turn-1")

	result := h.renderCodexStatus(runtime)
	for _, want := range []string{
		"- platform=feishu account=app-a chat=chat-a reply_to=msg-a；跟踪当前 turn",
		"- platform=feishu account=app-b chat=chat-b reply_to=msg-b；跟踪当前 turn",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
	if strings.Contains(result.Reply, "唯一 writer") {
		t.Fatalf("reply=%q, must not call one observer the unique writer", result.Reply)
	}
}

func TestCodexStatusOmitsReleasedFeishuObserverRoute(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"})
	runtime.req.Platform = platform.PlatformFeishu
	runtime.req.AccountID = "app-status"
	runtime.req.Reply = &codexFollowerRouteReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		route:   platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "app-status", ChatID: "chat-status"},
	}
	addCodexStatusFollower(t, h, runtime, "route-1", platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "app-a", ChatID: "chat-released", ReplyToID: "msg-released",
	}, "turn-1")
	if _, err := h.ensureCodexSessions().releaseWorkspaceThread(runtime.bindingKey, runtime.workspaceRoot); err != nil {
		t.Fatalf("release follower route: %v", err)
	}

	result := h.renderCodexStatus(runtime)
	if strings.Contains(result.Reply, "chat-released") || strings.Contains(result.Reply, "msg-released") {
		t.Fatalf("reply=%q, released observer route must be absent", result.Reply)
	}
	if !strings.Contains(result.Reply, "Turn 观察通道: 当前没有已登记的飞书观察通道") {
		t.Fatalf("reply=%q, want no active Feishu observer", result.Reply)
	}
}

func TestCodexStatusMarksUntraceableCommandRoute(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.setBindingState(agent.CodexThreadState{ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1"})
	result := h.renderCodexStatus(runtime)
	if !strings.Contains(result.Reply, "消息通道（本次命令）: platform=wechat；route 未暴露，chat/reply_to 不可追踪") {
		t.Fatalf("reply=%q, want untraceable WeChat route", result.Reply)
	}
	if !strings.Contains(result.Reply, "Turn 观察通道: 当前没有已登记的飞书观察通道") {
		t.Fatalf("reply=%q, want no Feishu observer for non-Feishu command", result.Reply)
	}
}

func TestCodexStatusTimeoutReleasesThreadLock(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.inspectRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runtime.ctx = ctx

	result := h.renderCodexStatus(runtime)
	if !strings.Contains(result.Reply, "任务: 未确认") ||
		!strings.Contains(result.Reply, "运行通道: 不可用") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexThreadLockReusable(t, h, "thread-1")
}

func TestCodexStatusInternalControlTimeoutReleasesThreadLock(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	h.codexControlTimeout = 20 * time.Millisecond
	ag.inspectRelease = make(chan struct{})

	result := h.renderCodexStatus(runtime)
	if !strings.Contains(result.Reply, "任务: 未确认") ||
		!strings.Contains(result.Reply, "运行通道: 不可用") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexThreadLockReusable(t, h, "thread-1")
}

func TestCompactCodexRuntimeStatusLinesPreserveTaskAndRuntimeFailures(t *testing.T) {
	tests := []struct {
		name       string
		resolution codexRuntimeResolution
		task       string
		runtime    string
	}{
		{
			name:       "idle",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw}},
			task:       "任务: 空闲",
			runtime:    "运行通道: 可用",
		},
		{
			name: "active",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{
				Runtime: agent.CodexRuntimeWeClaw,
				State:   agent.CodexThreadState{Active: true},
			}},
			task:    "任务: 正在执行",
			runtime: "运行通道: 可用",
		},
		{
			name:       "conflict",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeConflict}},
			task:       "任务: 空闲",
			runtime:    "运行通道: 不可用（Host 冲突）",
		},
		{
			name:       "desktop",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeDesktop}},
			task:       "任务: 空闲",
			runtime:    "运行通道: 可用（Codex App）",
		},
		{
			name:       "unknown",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeUnknown}},
			task:       "任务: 空闲",
			runtime:    "运行通道: 不可用（未确认）",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, runtime := compactCodexRuntimeStatusLines(test.resolution)
			if task != test.task || runtime != test.runtime {
				t.Fatalf("task=%q runtime=%q, want task=%q runtime=%q", task, runtime, test.task, test.runtime)
			}
		})
	}
}

func TestCodexDesktopRuntimeIsReadyForRemoteTurn(t *testing.T) {
	resolution := codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeDesktop}}
	if err := ensureCodexRuntimeReady(resolution, codexConversationRoute{}); err != nil {
		t.Fatalf("ensureCodexRuntimeReady() error = %v", err)
	}
	if !codexRuntimeReadyForRemoteTurn(agent.CodexRuntimeDesktop) {
		t.Fatal("Codex Desktop runtime must be writable through the IPC bridge")
	}
}

func assertCodexThreadLockReusable(t *testing.T, h *Handler, threadID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	unlock, err := h.lockCodexThreadControlContext(ctx, threadID)
	if err != nil {
		t.Fatalf("thread 控制锁无法复用: %v", err)
	}
	unlock()
}

func TestRemovedCodexOwnerReturnsSessionHelp(t *testing.T) {
	h, _, runtime := codexRuntimeStatusFixture(t)
	runtime.fields = []string{"/cx", "owner", "desktop"}
	result := h.dispatchCodexSessionCommand(runtime)
	if !strings.Contains(result.Reply, "Codex 会话命令") || strings.Contains(result.Reply, "无需释放 Codex 控制权") {
		t.Fatalf("reply=%q", result.Reply)
	}
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || threadID != "thread-1" {
		t.Fatalf("binding changed: thread=%q pending=%v", threadID, pending)
	}
}

func codexRuntimeStatusFixture(t *testing.T) (*Handler, *fakeCodexLiveAgent, codexSessionCommandRuntime) {
	t.Helper()
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-1"})
	bindingKey := codexBindingKey("route-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-1")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	runtime := codexSessionCommandRuntime{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		fields: []string{"/cx", "status"}, agentName: "codex", agent: ag,
		bindingKey: bindingKey, workspaceRoot: workspace,
		req: codexSessionCommandRequest{
			ActorUserID: "user-1", RouteUserID: "route-1",
			Platform: platform.PlatformWeChat,
			Reply:    platformtest.NewReplier(platform.Capabilities{Text: true}),
		},
	}
	return h, ag, runtime
}

func addCodexStatusFollower(t *testing.T, h *Handler, runtime codexSessionCommandRuntime, routeUserID string, route platform.DeliveryRoute, turnID string) {
	t.Helper()
	bindingKey := codexBindingKey(routeUserID, runtime.agentName)
	store := h.ensureCodexSessions()
	store.ensureWorkspace(bindingKey, runtime.workspaceRoot)
	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: runtime.workspaceRoot,
		TargetThreadID: "thread-1", ConversationID: buildCodexConversationID(routeUserID, runtime.agentName, runtime.workspaceRoot),
		SetFollower: true, Follower: &codexFrontendFollower{
			WorkspaceRoot: runtime.workspaceRoot, ThreadID: "thread-1", ActorUserID: routeUserID,
			AuthorizedIdentity: routeUserID, DeliveryRoute: route,
		},
		FollowerTurnID: turnID, FollowerTurnInitialized: true, FollowerTurnPending: true,
		Expected: store.remoteSelectionSnapshot(bindingKey, "thread-1"),
	})
	if err != nil {
		t.Fatalf("commit Feishu follower route: %v", err)
	}
	snapshot, ok := store.followerSnapshot(bindingKey)
	if !ok {
		t.Fatal("follower snapshot missing after commit")
	}
	prepared, err := store.commitFollowerAttachRuntime(snapshot, turnID, 0)
	if err != nil {
		t.Fatalf("prepare Feishu follower attach: %v", err)
	}
	if err := store.commitFollowerAttachReady(prepared, turnID, 0); err != nil {
		t.Fatalf("ready Feishu follower attach: %v", err)
	}
}
