package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

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
	case <-time.After(time.Second):
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
	case <-time.After(time.Second):
		t.Fatal("释放绑定锁后会话命令仍未继续")
	}
}

func TestCodexNewUsesBindingLock(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{})
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	unlock := h.lockAgentExecution(codexBindingExecutionKey(codexBindingKey("user-1", "codex")))
	done := make(chan struct{})
	go func() {
		h.resetDefaultCodexSessionForRoute(context.Background(), "user-1", "user-1", "codex", ag)
		close(done)
	}()
	assertNotClosed(t, done, "/new 越过了 Codex 绑定锁")
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("释放绑定锁后 /new 仍未继续")
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
	case <-time.After(time.Second):
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
