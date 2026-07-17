package messaging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexOwnerRemoteUsesSelectionAcquireTransaction(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	runtime := codexOwnerRuntimeFromAcquireFixture(fixture, "thread-b")
	runtime.fields = []string{"/cx", "owner", "remote"}

	result := fixture.h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "已切换并接管") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("thread-a intent=%#v，兼容入口应释放同 route 旧所有权", got)
	}
	if got := fixture.h.codexSessions.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("thread-b intent=%#v", got)
	}
}

func TestCodexOwnerRemoteRejectsActiveOldThreadWithoutStateChange(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	routeA := fixture.request("thread-a").route
	task, _, started := fixture.h.beginActiveTask(context.Background(), routeA.conversationID, activeTaskMeta{
		owner: fixture.routeUser, runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("创建旧会话活动任务失败")
	}
	defer fixture.h.finishActiveTask(routeA.conversationID, task)
	want := fixture.snapshot()
	runtime := codexOwnerRuntimeFromAcquireFixture(fixture, "thread-b")
	runtime.fields = []string{"/cx", "owner", "remote"}

	result := fixture.h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "当前远程任务仍在执行") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexAcquireState(t, fixture, want)
}

func TestCodexOwnerDesktopReleasesButKeepsSelection(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	h.codexSessions.setActiveWorkspace(runtime.bindingKey, runtime.workspaceRoot)
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "已归还给 Codex Desktop") {
		t.Fatalf("reply=%q", result.Reply)
	}
	threadID, pending := h.codexSessions.getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || threadID != "thread-1" {
		t.Fatalf("thread=%q pending=%v，显式释放必须保留选择", threadID, pending)
	}
	if active, _ := h.codexSessions.getActiveWorkspace(runtime.bindingKey); active != runtime.workspaceRoot {
		t.Fatalf("active=%q，显式释放不应改变活动工作空间", active)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlDesktop {
		t.Fatalf("intent=%#v", intent)
	}
	if ag.handoffCalls != 1 {
		t.Fatalf("handoff=%d", ag.handoffCalls)
	}
}

func TestCodexOwnerDesktopRejectsOtherRouteWithoutIdentityLeak(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	otherBinding := codexBindingKey("route-2", "codex")
	h.codexSessions.setThread(otherBinding, runtime.workspaceRoot, "thread-1")
	runtime.actorUserID = "user-2"
	runtime.routeUserID = "route-2"
	runtime.bindingKey = otherBinding
	runtime.ownerBindingKey = otherBinding
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "当前窗口未控制") ||
		strings.Contains(result.Reply, "route-1") || strings.Contains(result.Reply, "user-1") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if ag.handoffCalls != 0 {
		t.Fatalf("handoff=%d", ag.handoffCalls)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlRemote || intent.RouteBindingKey == otherBinding {
		t.Fatalf("intent=%#v", intent)
	}
}

func TestCodexOwnerDesktopRejectsActiveTaskWithoutStateChange(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	route := runtime.codexRoute("thread-1")
	task, _, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: runtime.actorUserID, runtimeOwner: agent.CodexRuntimeDesktop,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
	})
	if !started {
		t.Fatal("创建活动任务失败")
	}
	defer h.finishActiveTask(route.conversationID, task)
	wantIntent := h.codexSessions.controlIntent("thread-1")
	wantBinding := ag.threadBinding("thread-1")
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "当前远程任务结束") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if got := h.codexSessions.controlIntent("thread-1"); got != wantIntent {
		t.Fatalf("intent=%#v want=%#v", got, wantIntent)
	}
	if got := ag.threadBinding("thread-1"); got != wantBinding || ag.handoffCalls != 0 {
		t.Fatalf("binding=%#v handoff=%d", got, ag.handoffCalls)
	}
	threadID, pending := h.codexSessions.getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || threadID != "thread-1" {
		t.Fatalf("thread=%q pending=%v", threadID, pending)
	}
}

func TestCodexReselectAfterDesktopReleaseAcquiresAgain(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	runtime.fields = []string{"/cx", "owner", "desktop"}
	if result := h.handleCodexOwnerCommand(runtime); !strings.Contains(result.Reply, "已归还") {
		t.Fatalf("release reply=%q", result.Reply)
	}

	result := h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: runtime.routeUserID, agentName: runtime.agentName,
		workspaceRoot: runtime.workspaceRoot, agent: ag, target: "thread-1",
		ownerBindingKey: runtime.ownerBindingKey,
		options: codexSwitchOptions{
			actorUserID: runtime.actorUserID, platform: platform.PlatformWeChat,
			reply: runtime.req.Reply,
		},
	})

	if !strings.Contains(result, "已切换并接管") {
		t.Fatalf("reply=%q", result)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlRemote {
		t.Fatalf("intent=%#v", intent)
	}
}

func TestCodexOwnerDesktopTimeoutKeepsReleaseWithoutSecondProbe(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	ag.handoffRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runtime.ctx = ctx
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	if ag.handoffCalls != 1 || ag.bindCalls != 0 {
		t.Fatalf("handoff=%d inspect=%d，超时后不得二次探测", ag.handoffCalls, ag.bindCalls)
	}
	if !strings.Contains(result.Reply, "已归还") || !strings.Contains(result.Reply, "远程写入已关闭") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlDesktop {
		t.Fatalf("intent=%#v", intent)
	}
	if binding := ag.threadBinding("thread-1"); binding.Runtime == agent.CodexRuntimeConflict {
		t.Fatalf("释放确认超时不能伪造写入冲突，binding=%#v", binding)
	}
}

func TestCodexOwnerDesktopRuntimeFailureKeepsPersistedRelease(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	ag.handoffErrors["thread-1"] = errors.New("移交失败")
	ag.inspectErrors["thread-1"] = errors.New("校准失败")
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "已归还") || !strings.Contains(result.Reply, "远程写入已关闭") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if ag.handoffCalls != 1 || ag.bindCalls != 0 {
		t.Fatalf("handoff=%d inspect=%d", ag.handoffCalls, ag.bindCalls)
	}
	if binding := ag.threadBinding("thread-1"); binding.Runtime == agent.CodexRuntimeConflict {
		t.Fatalf("释放确认失败不能伪造写入冲突，binding=%#v", binding)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlDesktop {
		t.Fatalf("intent=%#v", intent)
	}
}

func TestCodexOwnerDesktopPersistenceFailureSkipsRuntime(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	h.codexSessions.SetFilePath(t.TempDir())
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	if !strings.Contains(result.Reply, "控制权提交失败") || strings.Contains(result.Reply, "已归还") {
		t.Fatalf("reply=%q", result.Reply)
	}
	if ag.handoffCalls != 0 {
		t.Fatalf("handoff=%d，持久化失败前不得触碰 runtime", ag.handoffCalls)
	}
	if intent := h.codexSessions.controlIntent("thread-1"); intent.Owner != codexControlRemote {
		t.Fatalf("intent=%#v", intent)
	}
}

func codexOwnerRuntimeFromAcquireFixture(fixture *codexSessionAcquireFixture, threadID string) codexSessionCommandRuntime {
	request := fixture.request(threadID)
	return codexSessionCommandRuntime{
		ctx: request.ctx, externalTaskCtx: request.taskContext,
		actorUserID: request.actorUserID, routeUserID: request.routeUserID,
		agentName: request.agentName, agent: request.agent,
		bindingKey: request.route.bindingKey, ownerBindingKey: request.route.bindingKey,
		workspaceRoot: request.route.workspaceRoot,
		req: codexSessionCommandRequest{
			ActorUserID: request.actorUserID, RouteUserID: request.routeUserID,
			Platform: request.platform, AccountID: request.accountID, Reply: request.reply,
		},
	}
}

func codexRemoteOwnerCommandFixture(t *testing.T) (*Handler, *fakeCodexLiveAgent, codexSessionCommandRuntime) {
	t.Helper()
	h, ag, runtime := codexOwnerCommandFixture(t)
	route := runtime.codexRoute("thread-1")
	current := h.codexSessions.controlIntent("thread-1")
	committed, err := h.codexSessions.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-1", Owner: codexControlRemote,
		RouteBindingKey: runtime.bindingKey, ConversationID: route.conversationID,
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.setThreadBinding("thread-1", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeDesktop,
		Control: agentControlIntent(committed), State: agent.CodexThreadState{ThreadID: "thread-1"},
	})
	return h, ag, runtime
}
