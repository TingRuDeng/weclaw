package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestProgressSessionDetachFreezesCardWithoutTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0

	onProgress, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	onProgress("正在读取代码")
	waitForRolloverCondition(t, func() bool {
		return len(reply.stream.updateSnapshot()) > 0
	})

	notice := "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。"
	if err := session.detachWithoutTerminal(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	updatesBefore := len(reply.stream.updateSnapshot())
	onProgress("解绑后的迟到进度")
	_ = finish("解绑后的最终回答", false)
	reply.stream.mu.Lock()
	superseded := append([]string(nil), reply.stream.superseded...)
	failed := append([]string(nil), reply.stream.failed...)
	reply.stream.mu.Unlock()
	if len(superseded) != 1 || !strings.Contains(superseded[0], "本地 Codex 任务继续运行") {
		t.Fatalf("superseded=%#v", superseded)
	}
	if reply.stream.completedCount() != 0 || len(failed) != 0 {
		t.Fatalf("completed=%#v failed=%#v, detach must not emit a terminal state", reply.stream.completedSnapshot(), failed)
	}
	if got := len(reply.stream.updateSnapshot()); got != updatesBefore {
		t.Fatalf("updates after detach=%d, want %d", got, updatesBefore)
	}
}

func TestProgressSessionDetachPersistsSupersedeOnlyRecoveryBeforeDelivery(t *testing.T) {
	_, outbox, path, reply, _, finish, session := newDurableReanchorFixture(t)
	reply.stream.deliverErr = errors.New("temporary card update failure")
	reservationID := session.recoveryReservation

	if err := session.detachWithoutTerminal(
		context.Background(), "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。",
	); err != nil {
		t.Fatalf("detach should queue a durable retry: %v", err)
	}
	entry := outbox.entryLocked(reservationID)
	if entry == nil || entry.Stream != nil || entry.Checkpoint != nil || entry.Text != "" || entry.Notification != "" || len(entry.PendingSupersedes) != 1 {
		t.Fatalf("supersede-only entry=%#v", entry)
	}
	if reply.stream.completedCount() != 0 || finish("迟到结果", false) {
		t.Fatalf("detached stream emitted a terminal result: completed=%#v", reply.stream.completedSnapshot())
	}

	reloaded, err := newTerminalOutbox(path, platform.NewRegistry(nil))
	if err != nil {
		t.Fatal(err)
	}
	if due := reloaded.dueIDs(); len(due) != 0 {
		t.Fatalf("restart scheduled terminal recovery for released card: %v", due)
	}
	reloaded.now = func() time.Time { return time.Now().Add(time.Hour) }
	if pending := reloaded.duePendingStreamSupersedes(); len(pending) != 1 {
		t.Fatalf("restart pending supersedes=%#v, want one", pending)
	}
}

func TestCodexThreadReplacementRefreshesActiveStreamRecoveryTrace(t *testing.T) {
	h := NewHandler(nil, nil)
	path := filepath.Join(t.TempDir(), "terminal-outbox.json")
	route := platform.DeliveryRoute{Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat"}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	outbox, err := newTerminalOutbox(path, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	h.terminalOutbox = outbox
	trace := observability.TraceContext{
		TraceID: "trace-replace", ConversationID: "conversation", ThreadID: "thread-old",
	}
	ctx := observability.ContextWithTrace(context.Background(), trace)
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, _, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		ctx, reply, "", "codex", "/workspace/project", "执行任务", cfg,
	)
	if progress == nil || progress.activeRecoveryReservation() == "" {
		t.Fatal("active progress recovery was not reserved")
	}
	task := &activeAgentTask{
		codexThreadID: "thread-old", trace: trace, progress: progress,
	}
	task.replaceCodexThread("thread-old", "thread-new")

	entries, err := loadTerminalOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Trace == nil || entries[0].Trace.ThreadID != "thread-new" {
		t.Fatalf("recovery trace=%#v, want thread-new", entries)
	}
	progress.stopBackground()
}

func TestCodexFirstTurnReplacementRecoversTracePersistenceFailureAfterRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "codex-sessions.json")
	outboxPath := filepath.Join(t.TempDir(), "terminal-outbox.json")
	workspace := filepath.Join(t.TempDir(), "project")
	routeUserID := "feishu:bot:dm:chat:user"
	bindingKey := codexBindingKey(routeUserID, "codex")
	conversationID := buildCodexConversationID(routeUserID, "codex", workspace)
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat", ReplyToID: "message",
	}

	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(statePath)
	selection := first.ensureCodexSessions().remoteSelectionSnapshot(bindingKey, "thread-old")
	if _, err := first.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspace, ConversationID: conversationID,
		TargetThreadID: "thread-old", PendingFirstTurn: true, SetFollower: true,
		Follower: &codexFrontendFollower{
			WorkspaceRoot: workspace, ThreadID: "thread-old", ActorUserID: "user",
			DeliveryRoute: route,
		},
		Expected: selection,
	}); err != nil {
		t.Fatal(err)
	}
	reply := newOutboxTestReplier(route)
	reply.stream = &outboxTestStream{}
	outbox, err := newTerminalOutbox(outboxPath, newOutboxTestRegistry(route, reply))
	if err != nil {
		t.Fatal(err)
	}
	first.terminalOutbox = outbox
	trace := observability.TraceContext{
		TraceID: "trace-replace-restart", ConversationID: conversationID, ThreadID: "thread-old",
	}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0
	_, _, progress := first.startProgressSessionForWorkspaceAgentWithHandle(
		observability.ContextWithTrace(context.Background(), trace), reply, "user", "codex", workspace, "执行任务", cfg,
	)
	if progress == nil || progress.activeRecoveryReservation() == "" {
		t.Fatal("active progress recovery was not reserved")
	}
	task := &activeAgentTask{codexThreadID: "thread-old", trace: trace, progress: progress}
	// 让 session 替换成功、outbox trace 刷新失败，模拟两个持久化文件之间的真实故障窗口。
	outbox.path = filepath.Join(outboxPath, "blocked")
	if err := first.commitCodexFirstTurnReplacement(codexControlledTurnOptions{
		ctx: context.Background(), task: task,
		route: codexConversationRoute{
			bindingKey: bindingKey, workspaceRoot: workspace,
			conversationID: conversationID, threadID: "thread-old",
		},
	}, agent.CodexThreadRef{
		ConversationID: conversationID, ThreadID: "thread-old",
	}, agent.CodexThreadRef{
		ConversationID: conversationID, ThreadID: "thread-new",
	}); err != nil {
		t.Fatal(err)
	}
	progress.stopBackground()

	restarted := NewHandler(nil, nil)
	restarted.SetCodexSessionFile(statePath)
	snapshots := restarted.ensureCodexSessions().followerSnapshots()
	if len(snapshots) != 1 || snapshots[0].Target.ThreadID != "thread-new" {
		t.Fatalf("restarted followers=%#v", snapshots)
	}
	restartedReply := newOutboxTestReplier(route)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restarted.StartTerminalOutbox(ctx, newOutboxTestRegistry(route, restartedReply), outboxPath); err != nil {
		t.Fatal(err)
	}
	state := externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
		ThreadID: "thread-new", Active: true, ActiveTurnID: "turn-new",
	}}
	if err := restarted.reconcileCodexFollowerRecoveries(snapshots[0], state, restartedReply); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTerminalOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Stream != nil || loaded[0].Text != "" ||
		len(loaded[0].PendingSupersedes) != 1 || loaded[0].Trace == nil ||
		loaded[0].Trace.ThreadID != "thread-new" || loaded[0].Trace.TurnID != "turn-new" {
		t.Fatalf("first-turn recovery was not repaired after restart: %#v", loaded)
	}
	if due := restarted.currentTerminalOutbox().dueIDs(); len(due) != 0 {
		t.Fatalf("repaired active turn exposed false terminal recovery: %v", due)
	}
}
