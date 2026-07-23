package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexSessionBindingFixture struct {
	h          *Handler
	ag         *fakeCodexLiveAgent
	routeUser  string
	bindingKey string
	workspaceA string
	workspaceB string
	reply      *platformtest.Replier
}

func newCodexSessionBindingFixture(t *testing.T) *codexSessionBindingFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	f := &codexSessionBindingFixture{
		h: h, routeUser: "route-user",
		workspaceA: "/workspace/a", workspaceB: "/workspace/b",
		reply: platformtest.NewReplier(platform.Capabilities{Text: true}),
	}
	f.bindingKey = codexBindingKey(f.routeUser, "codex")
	h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceA, "thread-a")
	h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceB, "thread-b")
	h.ensureCodexSessions().setActiveWorkspace(f.bindingKey, f.workspaceA)
	f.ag = newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	for _, threadID := range []string{"thread-a", "thread-b"} {
		f.ag.setThreadBinding(threadID, agent.CodexThreadBinding{
			Runtime: agent.CodexRuntimeWeClaw,
			State:   agent.CodexThreadState{ThreadID: threadID},
		})
	}
	return f
}

func (f *codexSessionBindingFixture) request(threadID string) codexSessionAcquireRequest {
	workspace := f.workspaceB
	if threadID == "thread-a" {
		workspace = f.workspaceA
	}
	return codexSessionAcquireRequest{
		ctx: context.Background(), actorUserID: f.routeUser, routeUserID: f.routeUser,
		agentName: "codex", agent: f.ag,
		route: codexConversationRoute{
			bindingKey: f.bindingKey, workspaceRoot: workspace,
			conversationID: buildCodexConversationID(f.routeUser, "codex", workspace),
			threadID:       threadID,
		},
		platform: platform.PlatformWeChat, reply: f.reply,
	}
}

func (f *codexSessionBindingFixture) setActiveTarget(turnID string) {
	state := agent.CodexThreadState{ThreadID: "thread-b", Active: true, ActiveTurnID: turnID}
	f.ag.setBindingState(state)
	f.ag.setThreadBinding("thread-b", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: state})
}

func TestAcquireCodexSessionCommitsFrontendBindingAndSharedRuntime(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceB {
		t.Fatalf("active workspace=%q", active)
	}
	if threadID, pending := f.h.ensureCodexSessions().getThread(f.bindingKey, f.workspaceB); pending || threadID != "thread-b" {
		t.Fatalf("thread=%q pending=%v", threadID, pending)
	}
	requests := f.ag.handoffRequests()
	if len(requests) != 1 || requests[0].Ref.ThreadID != "thread-b" ||
		requests[0].Intent.RouteKey != f.bindingKey {
		t.Fatalf("bind requests=%#v", requests)
	}
	if result.resolution.Binding.Runtime != agent.CodexRuntimeWeClaw {
		t.Fatalf("runtime=%q", result.resolution.Binding.Runtime)
	}
}

func TestAcquireCodexSessionRuntimeFailureKeepsFrontendBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.ag.handoffErrors["thread-b"] = context.DeadlineExceeded
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || !errors.Is(result.runtimeErr, context.DeadlineExceeded) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if threadID, _ := f.h.ensureCodexSessions().getThread(f.bindingKey, f.workspaceB); threadID != "thread-b" {
		t.Fatalf("binding rolled back to %q", threadID)
	}
	if got := len(f.ag.handoffRequests()); got != 1 {
		t.Fatalf("shared host bind retried %d times", got)
	}
	if f.ag.threadBinding("thread-b").Runtime == agent.CodexRuntimeConflict {
		t.Fatal("transport timeout was promoted to writer conflict")
	}
}

func TestAcquireCodexSessionPersistenceFailureSkipsRuntime(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.h.ensureCodexSessions().SetFilePath(filepath.Join(t.TempDir(), "codex-sessions.json"))
	f.h.ensureCodexSessions().writeState = func(string, []byte) error { return errors.New("disk full") }
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err == nil {
		t.Fatal("persistence failure was ignored")
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("live binding changed to %q", active)
	}
	if len(f.ag.handoffRequests()) != 0 {
		t.Fatal("runtime touched before durable binding commit")
	}
}

func TestAcquireCodexSessionAgentSelectionFailureRollsBackBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	statePath := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := f.h.SetAgentSessionFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := f.h.ensureAgentSessions().Set(f.routeUser, "claude"); err != nil {
		t.Fatal(err)
	}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.h.ensureAgentSessions().filePath = filepath.Join(invalidParent, "state.json")
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err == nil {
		t.Fatal("agent selection failure was ignored")
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("binding was not rolled back: %q", active)
	}
	if len(f.ag.handoffRequests()) != 0 {
		t.Fatal("runtime touched after agent selection failure")
	}
}

func TestAcquireCodexSessionSameConversationActiveTurnBlocksRebind(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	request := f.request("thread-b")
	f.h.ensureCodexSessions().setThread(f.bindingKey, f.workspaceB, "thread-a")
	task, _, started := f.h.beginActiveTask(context.Background(), request.route.conversationID, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex",
		codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("failed to create active task")
	}
	defer f.h.finishActiveTask(request.route.conversationID, task)
	_, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err == nil || !strings.Contains(err.Error(), "任务执行期间不能切换") {
		t.Fatalf("error=%v", err)
	}
}

func TestAcquireCodexSessionDifferentFrontendDoesNotAbandonRunningTask(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	oldConversation := buildCodexConversationID(f.routeUser, "codex", f.workspaceA)
	task, _, started := f.h.beginActiveTask(context.Background(), oldConversation, activeTaskMeta{
		owner: f.routeUser, routeUserID: f.routeUser, agentName: "codex",
		codexThreadID: "thread-a", codexTurnID: "turn-a",
	})
	if !started {
		t.Fatal("failed to create old task")
	}
	defer f.h.finishActiveTask(oldConversation, task)
	if _, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b")); err != nil {
		t.Fatal(err)
	}
	if current, active := f.h.activeTask(oldConversation); !active || current != task {
		t.Fatal("binding another conversation abandoned a running task")
	}
}

func TestAcquireCodexSessionActiveSharedTurnStartsObserver(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.setActiveTarget("turn-b")
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	result, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if err != nil || result.runtimeErr != nil || !result.externalActive {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.externalProgressCard {
		t.Fatal("text-only WeChat observer should keep inline task details")
	}
	task, active := f.h.activeTask(result.route.conversationID)
	if !active || task.codexThreadID != "thread-b" || task.codexTurnID != "turn-b" {
		t.Fatalf("active=%v task=%#v", active, task)
	}
	closeTestChannel(f.ag.watchDone)
	waitDone(t, task.done, "shared host observer cleanup")
}

func TestAcquireCodexSessionFeishuActiveTurnUsesDedicatedProgressCard(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.reply = platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	f.setActiveTarget("turn-b")
	f.ag.watchDone = make(chan struct{})
	t.Cleanup(func() { closeTestChannel(f.ag.watchDone) })
	request := f.request("thread-b")
	request.platform = platform.PlatformFeishu
	request.accountID = "cli_a"
	request.reply = f.reply

	result, err := f.h.acquireCodexSessionWithBindingLocked(request)
	if err != nil || result.runtimeErr != nil || !result.externalActive || !result.externalProgressCard {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	text := f.h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "进度和结果见下方任务卡") || strings.Contains(text, "共享 Codex 任务正在进行") {
		t.Fatalf("text=%q, want compact dedicated task card notice", text)
	}

	closeTestChannel(f.ag.watchDone)
	task, active := f.h.activeTask(result.route.conversationID)
	if active {
		waitDone(t, task.done, "feishu shared host observer cleanup")
	}
}

func TestCodexSwitchCommandRendersBindingSemantics(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	text := f.h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: f.routeUser, agentName: "codex",
		workspaceRoot: f.workspaceB, agent: f.ag, target: "thread-b",
		options: codexSwitchOptions{actorUserID: f.routeUser, platform: platform.PlatformFeishu, reply: f.reply},
	})
	if !strings.Contains(text, "已切换并绑定") ||
		strings.Contains(text, "窗口绑定") || strings.Contains(text, "运行位置") ||
		strings.Contains(text, "控制方") || strings.Contains(text, "接管") {
		t.Fatalf("text=%q", text)
	}
}

func TestRenderCodexSessionAcquireResultKeepsProgressInDedicatedTaskCard(t *testing.T) {
	h := NewHandler(nil, nil)
	result := codexSessionAcquireResult{
		route:                codexConversationRoute{workspaceRoot: "/workspace/card-manager-android", threadID: "thread-active"},
		externalActive:       true,
		externalProgressCard: true,
		externalState: externalCodexTaskState{
			CodexThreadState: agent.CodexThreadState{Preview: "好，你推进吧"},
			Progress:         "正在精简活动卡片",
		},
	}

	text := h.renderCodexSessionAcquireSuccess(result)
	for _, want := range []string{
		"已切换并绑定", "工作空间: card-manager-android",
		"模型: 未知（会话未记录） · 推理强度: 未知（会话未记录）",
		"运行中任务: 进度和结果见下方任务卡",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text=%q, want %q", text, want)
		}
	}
	for _, duplicate := range []string{
		"窗口绑定:", "运行位置:", "共享 Codex 任务正在进行", "任务: 好，你推进吧",
		"当前进展:", "任务完成后结果会自动返回",
	} {
		if strings.Contains(text, duplicate) {
			t.Fatalf("text=%q, should not repeat %q", text, duplicate)
		}
	}
}

func TestRenderCodexSessionAcquireResultExplainsReanchoredTaskCard(t *testing.T) {
	h := NewHandler(nil, nil)
	result := codexSessionAcquireResult{
		route:                codexConversationRoute{workspaceRoot: "/workspace/card-manager-android", threadID: "thread-active"},
		externalActive:       true,
		externalProgressCard: true,
		progressReanchored:   true,
	}

	text := h.renderCodexSessionAcquireSuccess(result)
	if !strings.Contains(text, "运行中任务: 已移到当前消息底部继续更新") {
		t.Fatalf("text=%q, want reanchored task card notice", text)
	}
	for _, duplicate := range []string{"共享 Codex 任务正在进行", "\n\n任务:", "当前进展:"} {
		if strings.Contains(text, duplicate) {
			t.Fatalf("text=%q, should not repeat %q", text, duplicate)
		}
	}
}

func TestAcquireCodexSessionTargetLockTimeoutKeepsBinding(t *testing.T) {
	f := newCodexSessionBindingFixture(t)
	f.h.codexLockWaitTimeout = 20 * time.Millisecond
	unlock := f.h.lockCodexThreadControl("thread-b")
	defer unlock()
	_, err := f.h.acquireCodexSessionWithBindingLocked(f.request("thread-b"))
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("error=%v", err)
	}
	if active, _ := f.h.ensureCodexSessions().getActiveWorkspace(f.bindingKey); active != f.workspaceA {
		t.Fatalf("binding changed to %q", active)
	}
}
