package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

const codexBindingTestCompletionTimeout = 2 * time.Second

// TestCodexTaskWaitsForBindingMutation 验证普通消息不会读取尚未提交的 Codex route。
func TestCodexTaskWaitsForBindingMutation(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	bindingKey := codexBindingKey(opts.routeUserID, opts.agentName)
	unlock := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	started := make(chan struct{})
	go func() {
		h.startCodexAgentTask(opts)
		close(started)
	}()
	assertNotClosed(t, started, "Codex 任务越过了会话绑定变更")
	unlock()
	select {
	case <-started:
	case <-time.After(codexBindingTestCompletionTimeout):
		t.Fatal("绑定提交后 Codex 任务仍未启动")
	}
	waitUntil(t, func() bool { return ag.chatCallCount() == 1 })
}

// TestCodexSessionCommandUsesBindingLock 验证会话命令与普通任务共享稳定窗口锁。
func TestCodexSessionCommandUsesBindingLock(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{})
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.agents["codex"] = ag
	h.defaultName = "codex"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	bindingKey := codexBindingKey("user-1", "codex")
	unlock := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	done := make(chan struct{})
	go func() {
		h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
			ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx whoami", Reply: reply,
		})
		close(done)
	}()
	assertNotClosed(t, done, "Codex 会话命令未使用稳定绑定锁")
	unlock()
	select {
	case <-done:
	case <-time.After(codexBindingTestCompletionTimeout):
		t.Fatal("释放绑定锁后会话命令仍未继续")
	}
}

func TestCodexSessionCommandBindingLockTimeout(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{})
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.agents["codex"] = ag
	h.defaultName = "codex"
	h.codexCommandTimeout = time.Second
	h.codexLockWaitTimeout = 20 * time.Millisecond
	bindingKey := codexBindingKey("user-1", "codex")
	unlock := h.lockAgentExecution(codexBindingExecutionKey(bindingKey))
	defer unlock()

	started := time.Now()
	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx owner",
	})
	if !strings.Contains(reply, "前一项 Codex 会话操作仍在处理") || !strings.Contains(reply, "本次命令未执行") {
		t.Fatalf("reply=%q", reply)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("锁等待未及时结束: %v", elapsed)
	}
}

func TestCodexSessionCommandSwitchTimeoutKeepsBindingAndReleasesLock(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.agents["codex"] = ag
	h.defaultName = "codex"
	h.codexCommandTimeout = 80 * time.Millisecond
	h.codexLockWaitTimeout = 20 * time.Millisecond
	ag.handoffEntered = make(chan struct{}, 1)
	ag.handoffRelease = make(chan struct{})

	switchResult := make(chan string, 1)
	go func() {
		switchResult <- h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
			ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx switch thread-1",
		})
	}()
	waitDone(t, ag.handoffEntered, "switch runtime 移交")

	ownerReply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx owner",
	})
	if !strings.Contains(ownerReply, "本次命令未执行") {
		t.Fatalf("owner reply=%q", ownerReply)
	}

	select {
	case reply := <-switchResult:
		if !strings.Contains(reply, "已切换并绑定") || !strings.Contains(reply, "运行通道: 暂不可用") {
			t.Fatalf("switch reply=%q", reply)
		}
	case <-time.After(codexBindingTestCompletionTimeout):
		t.Fatal("switch 未在总时限后释放 binding 锁")
	}
	if threadID, _ := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace); threadID != "thread-1" {
		t.Fatalf("运行通道超时后窗口 binding=%q", threadID)
	}
	assertExecutionLockReusable(t, h, codexBindingExecutionKey(codexBindingKey("user-1", "codex")))
}

func TestCodexNewUsesBindingLock(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	unlock := h.lockAgentExecution(codexBindingExecutionKey(codexBindingKey("user-1", "codex")))
	done := make(chan struct{})
	go func() {
		h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
			ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx new",
		})
		close(done)
	}()
	assertNotClosed(t, done, "/new 越过了 Codex 绑定锁")
	if resetCalls, _ := ag.resetSnapshot(); resetCalls != 0 || len(ag.handoffRequests()) != 0 {
		t.Fatalf("持锁时 reset=%d handoff=%d，期望均为 0", resetCalls, len(ag.handoffRequests()))
	}
	unlock()
	select {
	case <-done:
	case <-time.After(codexBindingTestCompletionTimeout):
		t.Fatal("释放绑定锁后 /new 仍未继续")
	}
	if resetCalls, _ := ag.resetSnapshot(); resetCalls != 1 || len(ag.handoffRequests()) != 1 {
		t.Fatalf("解锁后 reset=%d handoff=%d，期望均为 1", resetCalls, len(ag.handoffRequests()))
	}
}

func TestHandleCodexNewRejectsActiveOldRemoteTaskBeforeReset(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-old")
	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "codex", codexThreadID: "thread-old",
	})
	if !started {
		t.Fatal("未能建立旧会话 active task")
	}
	defer h.finishActiveTask(conversationID, task)

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx new",
	})
	if resetCalls, _ := ag.resetSnapshot(); resetCalls != 0 {
		t.Fatalf("旧任务活动时 ResetSession 调用=%d，期望 0", resetCalls)
	}
	if !strings.Contains(reply, "当前会话任务仍在执行") {
		t.Fatalf("reply=%q", reply)
	}
}

func TestCwdUsesCodexBindingLock(t *testing.T) {
	h, ag, _ := codexLiveSwitchFixture(t, agent.CodexThreadState{})
	h.agents["codex"] = ag
	dir := t.TempDir()
	unlock := h.lockAgentExecution(codexBindingExecutionKey(codexBindingKey("user-1", "codex")))
	done := make(chan struct{})
	go func() {
		h.handleCwdWithAccess("/cwd "+dir, []string{"user-1"}, true)
		close(done)
	}()
	assertNotClosed(t, done, "/cwd 越过了 Codex 绑定锁")
	unlock()
	select {
	case <-done:
	case <-time.After(codexBindingTestCompletionTimeout):
		t.Fatal("释放绑定锁后 /cwd 仍未继续")
	}
}

func assertNotClosed(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
		t.Fatal(message)
	case <-time.After(100 * time.Millisecond):
	}
}
