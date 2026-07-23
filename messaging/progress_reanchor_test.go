package messaging

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

type reanchorTestReplier struct {
	mu          sync.Mutex
	stream      *reanchorTestStream
	openCalls   int
	lastOptions platform.StreamOptions
	cardID      string
}

func newReanchorTestReplier() *reanchorTestReplier {
	return &reanchorTestReplier{stream: &reanchorTestStream{}}
}

func (r *reanchorTestReplier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true, Streaming: true}
}

func (r *reanchorTestReplier) SendText(context.Context, string) error  { return nil }
func (r *reanchorTestReplier) SendImage(context.Context, string) error { return nil }
func (r *reanchorTestReplier) SendFile(context.Context, string) error  { return nil }
func (r *reanchorTestReplier) Typing(context.Context, bool) error      { return nil }
func (r *reanchorTestReplier) AskChoices(context.Context, string, []platform.Choice) error {
	return nil
}

func (r *reanchorTestReplier) OpenStream(_ context.Context, opts platform.StreamOptions) (platform.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openCalls++
	r.lastOptions = opts
	return r.stream, nil
}

func (r *reanchorTestReplier) CurrentTaskCardID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cardID
}

func (r *reanchorTestReplier) BindTaskCard(cardID string) {
	r.mu.Lock()
	r.cardID = cardID
	r.mu.Unlock()
}

type reanchorTestStream struct {
	mu              sync.Mutex
	updates         []string
	completed       []string
	failed          []string
	superseded      []string
	prepared        []string
	completeStarted chan struct{}
	completeRelease <-chan struct{}
}

func (s *reanchorTestStream) Update(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, content)
	return nil
}

func (s *reanchorTestStream) Complete(_ context.Context, content string) error {
	if s.completeStarted != nil {
		select {
		case s.completeStarted <- struct{}{}:
		default:
		}
	}
	if s.completeRelease != nil {
		<-s.completeRelease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, content)
	return nil
}

func TestProgressSessionTerminalWinsConcurrentReanchor(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	oldReply.cardID = "card-old"
	newReply.cardID = "card-new"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	oldReply.stream.completeStarted = started
	oldReply.stream.completeRelease = release
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)

	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	finishDone := make(chan bool, 1)
	go func() { finishDone <- finish("最终结果", false) }()
	<-started

	type moveResult struct {
		moved bool
		err   error
	}
	moveDone := make(chan moveResult, 1)
	go func() {
		moved, err := session.reanchor(context.Background(), newReply, "进展 A")
		moveDone <- moveResult{moved: moved, err: err}
	}()
	close(release)

	if !<-finishDone {
		t.Fatal("terminal should be consumed by old authoritative stream")
	}
	move := <-moveDone
	if move.err != nil || move.moved || newReply.openCalls != 0 {
		t.Fatalf("terminal must prevent a late reanchor: result=%#v calls=%d", move, newReply.openCalls)
	}
	if oldReply.stream.completedCount() != 1 || oldReply.stream.supersededCount() != 0 {
		t.Fatalf("old completed=%d superseded=%d", oldReply.stream.completedCount(), oldReply.stream.supersededCount())
	}
}

func (s *reanchorTestStream) Fail(_ context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, content)
	return nil
}

func (s *reanchorTestStream) Supersede(_ context.Context, notice string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.superseded = append(s.superseded, notice)
	return nil
}

func (s *reanchorTestStream) PrepareTerminal(content string, failed bool) (platform.TerminalCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepared = append(s.prepared, content)
	return platform.TerminalCheckpoint{Kind: "reanchor.test.terminal"}, nil
}

func TestProgressSessionReanchorMovesUpdatesAndTerminalToNewStream(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	oldReply.cardID = "card-old"
	newReply.cardID = "card-new"
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	cfg.InitialDelaySeconds = 0

	_, finish, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if !session.send("进展 A") {
		t.Fatal("initial progress should be sent")
	}

	moved, err := session.reanchor(context.Background(), newReply, "进展 A")
	if err != nil || !moved {
		t.Fatalf("reanchor moved=%t err=%v", moved, err)
	}
	if oldReply.stream.supersededCount() != 1 {
		t.Fatalf("old stream superseded=%d, want 1", oldReply.stream.supersededCount())
	}
	if oldReply.CurrentTaskCardID() != "card-new" {
		t.Fatalf("future task interactions still target %q, want card-new", oldReply.CurrentTaskCardID())
	}
	if newReply.openCalls != 1 || !strings.Contains(newReply.lastOptions.InitialContent, "进展 A") {
		t.Fatalf("new stream calls=%d opts=%#v", newReply.openCalls, newReply.lastOptions)
	}

	if !session.send("进展 B") {
		t.Fatal("progress after reanchor should be sent")
	}
	if got := oldReply.stream.updateSnapshot(); len(got) != 1 || got[0] != "进展 A" {
		t.Fatalf("old updates=%#v, want only pre-reanchor progress", got)
	}
	if got := newReply.stream.updateSnapshot(); len(got) != 1 || got[0] != "进展 B" {
		t.Fatalf("new updates=%#v, want post-reanchor progress", got)
	}

	if !finish("最终结果", false) {
		t.Fatal("new stream should consume terminal result")
	}
	if oldReply.stream.completedCount() != 0 || newReply.stream.completedSnapshot()[0] != "最终结果" {
		t.Fatalf("old completed=%#v new completed=%#v", oldReply.stream.completedSnapshot(), newReply.stream.completedSnapshot())
	}
}

func TestProgressSessionDurableTerminalUsesReanchoredStream(t *testing.T) {
	h := NewHandler(nil, nil)
	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)

	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		context.Background(), oldReply, "", "codex", "/workspace/project-a", "执行任务", cfg,
	)
	if moved, err := session.reanchor(context.Background(), newReply, "最新进展"); err != nil || !moved {
		t.Fatalf("reanchor moved=%t err=%v", moved, err)
	}
	prepared, err := session.prepareDurableTerminal(newReply, "最终结果", false)
	if err != nil || prepared.checkpoint == nil || prepared.checkpoint.Kind != "reanchor.test.terminal" {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	if oldReply.stream.preparedCount() != 0 || newReply.stream.preparedCount() != 1 {
		t.Fatalf("old prepared=%d new prepared=%d", oldReply.stream.preparedCount(), newReply.stream.preparedCount())
	}
}

func TestCodexReturnToRunningTaskReanchorsOnce(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	workspaceA, workspaceB := "/workspace/project-a", "/workspace/project-b"
	threadA, threadB := "thread-a", "thread-b"
	bindingKey := codexBindingKey("route-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspaceA, threadA)
	h.ensureCodexSessions().setThread(bindingKey, workspaceB, threadB)
	conversationA := buildCodexConversationID("route-1", "codex", workspaceA)
	conversationB := buildCodexConversationID("route-1", "codex", workspaceB)
	h.commitCodexTaskCardFocus(bindingKey, conversationB)
	// 工作空间卡会在用户选择具体会话前预先更新持久化 workspace；任务卡
	// 重锚必须以最后一次完整接管的会话为准，不能被这一步导航状态吞掉。
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspaceA)

	state := agent.CodexThreadState{
		ThreadID: threadA, Active: true, ActiveTurnID: "turn-a", Preview: "项目 A 任务",
	}
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, state)
	route := codexConversationRoute{
		bindingKey: bindingKey, workspaceRoot: workspaceA,
		conversationID: conversationA, threadID: threadA,
	}
	trace := observability.TraceContext{ConversationID: route.conversationID}
	task, taskCtx, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", agentName: "codex", message: "项目 A 任务",
		codexThreadID: threadA, inProcessCodexLifecycle: true, trace: trace,
	})
	if !started {
		t.Fatal("active task should start")
	}
	defer h.finishActiveTask(route.conversationID, task)

	oldReply := newReanchorTestReplier()
	newReply := newReanchorTestReplier()
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	_, finish, progress := h.startProgressSessionForWorkspaceAgentWithHandle(
		taskCtx, oldReply, "", "codex", workspaceA, "项目 A 任务", cfg,
	)
	defer finish("", false)
	task.attachProgressSession(progress)
	task.recordProgressText(testNow(), "项目 A 最新进展")

	req := codexSessionAcquireRequest{
		ctx: context.Background(), taskContext: context.Background(),
		actorUserID: "user-1", routeUserID: "route-1", agentName: "codex", agent: ag,
		route: route, platform: platform.PlatformFeishu, reply: newReply,
	}
	result, err := h.acquireCodexSessionWithBindingLocked(req)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !result.progressReanchored || newReply.openCalls != 1 || oldReply.stream.supersededCount() != 1 {
		t.Fatalf("result=%#v new calls=%d old superseded=%d", result, newReply.openCalls, oldReply.stream.supersededCount())
	}
	if !strings.Contains(newReply.lastOptions.InitialContent, "项目 A 最新进展") {
		t.Fatalf("new card initial content=%q", newReply.lastOptions.InitialContent)
	}
	if rendered := h.renderCodexSessionAcquireSuccess(result); !strings.Contains(rendered, "已移到当前消息底部继续更新") {
		t.Fatalf("switch result must explain the new card position: %q", rendered)
	}

	result, err = h.acquireCodexSessionWithBindingLocked(req)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if result.progressReanchored || newReply.openCalls != 1 {
		t.Fatalf("same binding must not reanchor again: result=%#v calls=%d", result, newReply.openCalls)
	}
}

func (s *reanchorTestStream) updateSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.updates...)
}

func (s *reanchorTestStream) completedSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.completed...)
}

func (s *reanchorTestStream) supersededCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.superseded)
}

func (s *reanchorTestStream) completedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.completed)
}

func (s *reanchorTestStream) preparedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prepared)
}

func testNow() (now time.Time) {
	return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
}
