package messaging

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type admittedTaskExpectation struct {
	h          *Handler
	ag         *fakeCodexLiveAgent
	workspaceA string
	workspaceB string
}

type admissionRunner struct {
	h   *Handler
	ag  *fakeCodexLiveAgent
	ctx context.Context
	wg  *sync.WaitGroup
}

// TestCodexAcquireAndTaskAdmissionSerializeOnBinding 验证接管提交前普通任务不能登记或启动。
func TestCodexAcquireAndTaskAdmissionSerializeOnBinding(t *testing.T) {
	h, ag, workspaceA, workspaceB := newPlatformSelectionFixture(t, "route-serial")
	ag.setBindingState(agent.CodexThreadState{})
	ag.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	handoffRelease, turnRelease := make(chan struct{}), make(chan struct{})
	ag.handoffEntered, ag.handoffRelease = make(chan struct{}, 1), handoffRelease
	ag.turnEntered, ag.turnRelease = make(chan struct{}, 1), turnRelease
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	h.progressConfig = config.DefaultProgressConfig()
	h.progressConfig.Mode = progressModeOff
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		closeTestChannel(handoffRelease)
		closeTestChannel(turnRelease)
		cancel()
		wg.Wait()
	})
	runner := admissionRunner{h: h, ag: ag, ctx: ctx, wg: &wg}
	switchReply, switchDone := runner.startBlockedSelection(t)
	messageDone := runner.startBlockedOrdinaryMessage()
	waitForExecutionLockUsers(t, executionLockUsersExpectation{
		h: h, key: codexBindingExecutionKey(codexBindingKey("route-serial", "codex")), users: 2,
	})
	assertNoTaskAdmitted(t, h, ag)
	closeTestChannel(handoffRelease)
	waitDone(t, switchDone, "接管完成")
	if len(switchReply.Texts) != 1 || !strings.HasPrefix(switchReply.Texts[0], "已切换并接管。") {
		t.Fatalf("switch replies=%#v", switchReply.Texts)
	}
	waitDone(t, messageDone, "普通消息完成准入")
	waitDone(t, ag.turnEntered, "普通消息启动 B turn")
	task := assertAdmittedTaskUsesTarget(t, admittedTaskExpectation{h: h, ag: ag, workspaceA: workspaceA, workspaceB: workspaceB})
	closeTestChannel(turnRelease)
	waitDone(t, task.done, "B task 清理")
	wg.Wait()
}

func (r admissionRunner) startBlockedSelection(t *testing.T) (*platformtest.Replier, <-chan struct{}) {
	t.Helper()
	reply, done := platformtest.NewReplier(platform.Capabilities{Text: true}), make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.h.HandleMessage(r.ctx, platform.IncomingMessage{Platform: platform.PlatformWeChat, UserID: "route-serial", MessageID: "serial-switch", Text: "/cx switch thread-b"}, reply)
		close(done)
	}()
	waitDone(t, r.ag.handoffEntered, "接管进入 Handoff")
	return reply, done
}

func (r admissionRunner) startBlockedOrdinaryMessage() <-chan struct{} {
	done := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.h.HandleMessage(r.ctx, platform.IncomingMessage{Platform: platform.PlatformWeChat, UserID: "route-serial", MessageID: "serial-message", Text: "接管后继续任务"}, platformtest.NewReplier(platform.Capabilities{Text: true}))
		close(done)
	}()
	return done
}

func assertNoTaskAdmitted(t *testing.T, h *Handler, ag *fakeCodexLiveAgent) {
	t.Helper()
	ag.mu.Lock()
	runCalls := ag.runCalls
	ag.mu.Unlock()
	if countActiveTasks(h) != 0 || runCalls != 0 {
		t.Fatalf("接管未提交时任务越过 binding：active=%d run=%d", countActiveTasks(h), runCalls)
	}
}

func assertAdmittedTaskUsesTarget(t *testing.T, want admittedTaskExpectation) *activeAgentTask {
	t.Helper()
	target := want.h.codexSessions.controlIntent("thread-b")
	conversationID := buildCodexConversationID("route-serial", "codex", want.workspaceB)
	want.ag.mu.Lock()
	turnRequest := want.ag.lastTurnReq
	want.ag.mu.Unlock()
	if turnRequest.Runtime.Ref.ThreadID != "thread-b" || turnRequest.Runtime.Ref.ConversationID != conversationID ||
		turnRequest.Runtime.Intent.Revision != target.Revision || turnRequest.Runtime.Intent.RouteKey != target.RouteBindingKey {
		t.Fatalf("turn=%#v target=%#v", turnRequest, target)
	}
	task, active := want.h.activeTask(conversationID)
	if !active || task.codexThreadID != "thread-b" || task.routeUserID != "route-serial" {
		t.Fatalf("active=%t task=%#v target=%#v", active, task, target)
	}
	if _, oldActive := want.h.activeTask(buildCodexConversationID("route-serial", "codex", want.workspaceA)); oldActive {
		t.Fatal("普通消息错误登记到旧 A conversation")
	}
	return task
}
