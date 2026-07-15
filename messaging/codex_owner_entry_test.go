package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexOwnerEntryContextKey struct{}

func TestCodexOwnerRemoteRealEntryHidesAgentSessionStoreError(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": runtime.workspaceRoot})
	blockingParent := filepath.Join(t.TempDir(), "blocked-parent")
	if err := os.WriteFile(blockingParent, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	internalSessionPath := filepath.Join(
		blockingParent, "route-secret", "conversation-secret", "codex.sock", "state.json",
	)
	h.agentSessions.mu.Lock()
	h.agentSessions.filePath = internalSessionPath
	h.agentSessions.mu.Unlock()

	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: runtime.actorUserID, RouteUserID: runtime.routeUserID,
		Trimmed: "/cx owner remote", Platform: platform.PlatformWeChat,
		Reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
	})

	assertCodexOwnerReplySafe(t, result)
	if strings.Contains(result, blockingParent) {
		t.Fatalf("result=%q 泄露内部路径 %q", result, blockingParent)
	}
	if !strings.Contains(result, "已切换并接管") ||
		!strings.Contains(result, "警告: 保存当前窗口 Agent 失败") {
		t.Fatalf("result=%q", result)
	}
}

func TestCodexOwnerRemoteRealEntryCarriesTransactionContext(t *testing.T) {
	h := NewHandler(nil, nil)
	workspaceA, workspaceB := t.TempDir(), t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspaceA, workspaceB})
	state := agent.CodexThreadState{
		ThreadID: "thread-b", Active: true, ActiveTurnID: "turn-b", Preview: "活动任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, state)
	ag.watchDone = make(chan struct{})
	ag.watchReply = "活动任务完成"
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceB})
	routeUserID := "feishu:tenant:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	h.codexSessions.setActiveWorkspace(bindingKey, workspaceB)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: routeUserID, agentName: "codex", bindingKey: bindingKey,
		workspace: workspaceA, threadID: "thread-a",
	})
	claimDesktopControlForAcquireTest(t, h, "thread-b")
	ag.setThreadBinding("thread-a", desktopAcquireBinding("thread-a"))
	ag.setThreadBinding("thread-b", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeDesktop, State: state})
	lockHeld := false
	ag.handoffHooks["thread-b"] = func() {
		lockCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		unlock, err := h.lockCodexSessionBinding(lockCtx, bindingKey, "owner-entry-test")
		if err != nil {
			lockHeld = true
			return
		}
		unlock()
	}
	progressCfg := config.DefaultProgressConfig()
	progressCfg.Mode = progressModeOff
	h.SetPlatformProgressConfigs(map[string]config.ProgressConfig{
		PlatformAccountConfigKey(platform.PlatformFeishu, "account-a"): progressCfg,
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	base := context.WithValue(context.Background(), codexOwnerEntryContextKey{}, "task-context")
	ctx, cancel := context.WithCancel(base)
	defer cancel()

	result := h.handleCodexSessionCommandForRoute(ctx, codexSessionCommandRequest{
		ActorUserID: "actor-a", RouteUserID: routeUserID, Trimmed: "/cx owner remote",
		Platform: platform.PlatformFeishu, AccountID: "account-a", Reply: reply,
	})

	if !lockHeld || result == "" {
		t.Fatalf("lockHeld=%v result=%q", lockHeld, result)
	}
	if active, _ := h.codexSessions.getActiveWorkspace(bindingKey); active != workspaceB {
		t.Fatalf("active=%q", active)
	}
	if h.codexSessions.controlIntent("thread-a").Owner != codexControlDesktop ||
		h.codexSessions.controlIntent("thread-b").Owner != codexControlRemote {
		t.Fatalf("A=%#v B=%#v", h.codexSessions.controlIntent("thread-a"), h.codexSessions.controlIntent("thread-b"))
	}
	conversationID := buildCodexConversationID(routeUserID, "codex", workspaceB)
	task, active := h.activeTask(conversationID)
	if !active {
		t.Fatal("真实入口未启动活动 Desktop 任务观察")
	}
	opts := externalCodexRuntimeOptionsForTest(t, task)
	if opts.ctx != ctx || opts.platform != platform.PlatformFeishu ||
		opts.accountID != "account-a" || opts.reply != reply {
		t.Fatalf("opts=%#v", opts)
	}
	close(ag.watchDone)
	waitUntil(t, func() bool { _, active := h.activeTask(conversationID); return !active })
	if !containsText(reply.Texts, "活动任务完成") {
		t.Fatalf("texts=%#v", reply.Texts)
	}
}

func externalCodexRuntimeOptionsForTest(t *testing.T, task *activeAgentTask) externalCodexTaskOptions {
	t.Helper()
	task.mu.Lock()
	control := task.externalReservation
	task.mu.Unlock()
	if control == nil {
		t.Fatal("活动任务缺少 external reservation")
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.runtime.opts
}
