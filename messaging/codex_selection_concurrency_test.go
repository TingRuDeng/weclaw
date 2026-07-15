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

type integratedSelectionResult struct {
	index int
	text  string
}

type integratedSelectionOutcome struct {
	winner int
	loser  int
}

type concurrentSelectionFixture struct {
	h          *Handler
	ag         *fakeCodexLiveAgent
	routes     []string
	oldThreads []string
	workspaces []string
}

type admittedTaskExpectation struct {
	h          *Handler
	ag         *fakeCodexLiveAgent
	workspaceA string
	workspaceB string
}

// TestCodexRemoteSelectionConcurrentRoutesHaveOneIntegratedWinner 验证并发选择只有一个完整赢家。
func TestCodexRemoteSelectionConcurrentRoutesHaveOneIntegratedWinner(t *testing.T) {
	fixture := newConcurrentSelectionFixture(t)
	results := fixture.run(t)
	outcome := integratedSelectionWinner(t, results)
	fixture.assertWinner(t, results, outcome)
}

func newConcurrentSelectionFixture(t *testing.T) concurrentSelectionFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	root := t.TempDir()
	workspaces := []string{filepath.Join(root, "route-a"), filepath.Join(root, "route-b"), filepath.Join(root, "target")}
	for _, workspace := range workspaces {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("创建测试工作空间失败：%v", err)
		}
	}
	h.SetAllowedWorkspaceRoots([]string{root})
	h.SetAgentWorkDirs(map[string]string{"codex": workspaces[2]})
	h.codexSessions.SetFilePath(filepath.Join(t.TempDir(), "codex-sessions.json"))
	h.defaultName = "codex"
	routes := []string{"winner-candidate-a", "winner-candidate-b"}
	oldThreads := []string{"thread-a", "thread-c"}
	for index, route := range routes {
		bindingKey := codexBindingKey(route, "codex")
		h.codexSessions.setThread(bindingKey, workspaces[index], oldThreads[index])
		h.codexSessions.setThread(bindingKey, workspaces[2], "thread-b")
		h.codexSessions.setActiveWorkspace(bindingKey, workspaces[index])
		claimRemoteControlForTest(t, h, fakeRemoteControlOptions{routeUserID: route, agentName: "codex", bindingKey: bindingKey, workspace: workspaces[index], threadID: oldThreads[index]})
	}
	claimDesktopControlForAcquireTest(t, h, "thread-b")
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.setThreadBinding("thread-a", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: agent.CodexThreadState{ThreadID: "thread-a"}})
	ag.setThreadBinding("thread-c", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: agent.CodexThreadState{ThreadID: "thread-c"}})
	ag.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	h.agents["codex"] = ag
	return concurrentSelectionFixture{h: h, ag: ag, routes: routes, oldThreads: oldThreads, workspaces: workspaces}
}

func (f concurrentSelectionFixture) run(t *testing.T) []integratedSelectionResult {
	t.Helper()
	start := make(chan struct{})
	resultCh := make(chan integratedSelectionResult, len(f.routes))
	for index, route := range f.routes {
		go func(index int, route string) {
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			<-start
			f.h.HandleMessage(context.Background(), platform.IncomingMessage{
				Platform: platform.PlatformWeChat, AccountID: "wx-" + route,
				UserID: route, MessageID: "concurrent-select-" + route, Text: "/cx switch thread-b",
			}, reply)
			resultCh <- integratedSelectionResult{index: index, text: strings.Join(reply.Texts, "\n")}
		}(index, route)
	}
	close(start)
	got := make([]integratedSelectionResult, len(f.routes))
	for range f.routes {
		result := waitIntegratedSelectionResult(t, resultCh)
		got[result.index] = result
	}
	return got
}

func (f concurrentSelectionFixture) assertWinner(t *testing.T, got []integratedSelectionResult, outcome integratedSelectionOutcome) {
	t.Helper()
	winner, loser := outcome.winner, outcome.loser
	winnerBinding := codexBindingKey(f.routes[winner], "codex")
	loserBinding := codexBindingKey(f.routes[loser], "codex")
	target := f.h.codexSessions.controlIntent("thread-b")
	if target.Owner != codexControlRemote || target.RouteBindingKey != winnerBinding ||
		target.ConversationID != buildCodexConversationID(f.routes[winner], "codex", f.workspaces[2]) {
		t.Fatalf("target=%#v，winner route=%q", target, winnerBinding)
	}
	if old := f.h.codexSessions.controlIntent(f.oldThreads[winner]); old.Owner != codexControlDesktop {
		t.Fatalf("赢家旧 thread 未释放：%#v", old)
	}
	loserOld := f.h.codexSessions.controlIntent(f.oldThreads[loser])
	if loserOld.Owner != codexControlRemote || loserOld.RouteBindingKey != loserBinding {
		t.Fatalf("失败方旧状态被修改：%#v", loserOld)
	}
	if active, _ := f.h.codexSessions.getActiveWorkspace(loserBinding); active != f.workspaces[loser] {
		t.Fatalf("失败方 active=%q，want %q", active, f.workspaces[loser])
	}
	if f.ag.threadBinding(f.oldThreads[loser]).Runtime != agent.CodexRuntimeWeClaw ||
		f.ag.threadBinding(f.oldThreads[winner]).Control.Owner != agent.CodexControlDesktop {
		t.Fatalf("runtime 状态与赢家不一致：winner=%#v loser=%#v", f.ag.threadBinding(f.oldThreads[winner]), f.ag.threadBinding(f.oldThreads[loser]))
	}
	if strings.Contains(got[loser].text, f.routes[winner]) || strings.Contains(got[loser].text, winnerBinding) ||
		strings.Contains(got[loser].text, target.ConversationID) {
		t.Fatalf("失败回复泄露赢家身份：%q", got[loser].text)
	}
	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(f.h.codexSessions.filePath)
	if persisted := reloaded.controlIntent("thread-b"); persisted.RouteBindingKey != winnerBinding || persisted.Revision != target.Revision {
		t.Fatalf("persisted target=%#v，want route=%q revision=%d", persisted, winnerBinding, target.Revision)
	}
	if countRemoteCodexOwners(f.h.codexSessions) != 2 {
		t.Fatalf("应保留赢家 B 与失败方旧 thread 两个不同 route owner，controls=%#v", f.h.codexSessions.controls)
	}
	if _, active := f.h.activeTask(target.ConversationID); active {
		t.Fatal("inactive 并发目标不应创建 observer")
	}
}

func waitIntegratedSelectionResult(t *testing.T, results <-chan integratedSelectionResult) integratedSelectionResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("并发选择未结束")
		return integratedSelectionResult{}
	}
}

func integratedSelectionWinner(t *testing.T, results []integratedSelectionResult) integratedSelectionOutcome {
	t.Helper()
	success := []bool{strings.Contains(results[0].text, "已切换并接管"), strings.Contains(results[1].text, "已切换并接管")}
	if success[0] == success[1] {
		t.Fatalf("必须恰好一个成功：results=%#v", results)
	}
	if success[0] {
		if !strings.Contains(results[1].text, "其他远程窗口") {
			t.Fatalf("失败回复=%q", results[1].text)
		}
		return integratedSelectionOutcome{winner: 0, loser: 1}
	}
	if !strings.Contains(results[0].text, "其他远程窗口") {
		t.Fatalf("失败回复=%q", results[0].text)
	}
	return integratedSelectionOutcome{winner: 1, loser: 0}
}

// TestCodexAcquireAndTaskAdmissionSerializeOnBinding 验证接管提交前普通任务不能登记或启动。
func TestCodexAcquireAndTaskAdmissionSerializeOnBinding(t *testing.T) {
	h, ag, workspaceA, workspaceB := newPlatformSelectionFixture(t, "route-serial")
	ag.setBindingState(agent.CodexThreadState{})
	ag.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	ag.handoffEntered = make(chan struct{}, 1)
	handoffRelease := make(chan struct{})
	t.Cleanup(func() { closeTestChannel(handoffRelease) })
	ag.handoffRelease = handoffRelease
	ag.turnEntered = make(chan struct{}, 1)
	turnRelease := make(chan struct{})
	t.Cleanup(func() { closeTestChannel(turnRelease) })
	ag.turnRelease = turnRelease
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	h.progressConfig = config.DefaultProgressConfig()
	h.progressConfig.Mode = progressModeOff
	switchReply := platformtest.NewReplier(platform.Capabilities{Text: true})
	switchDone := make(chan struct{})
	go func() {
		h.HandleMessage(context.Background(), platform.IncomingMessage{
			Platform: platform.PlatformWeChat, UserID: "route-serial",
			MessageID: "serial-switch", Text: "/cx switch thread-b",
		}, switchReply)
		close(switchDone)
	}()
	waitDone(t, ag.handoffEntered, "接管进入 Handoff")

	messageAttempt := make(chan struct{})
	messageDone := make(chan struct{})
	go func() {
		messageAttempt <- struct{}{}
		h.HandleMessage(context.Background(), platform.IncomingMessage{
			Platform: platform.PlatformWeChat, UserID: "route-serial",
			MessageID: "serial-message", Text: "接管后继续任务",
		}, platformtest.NewReplier(platform.Capabilities{Text: true}))
		close(messageDone)
	}()
	<-messageAttempt
	ag.mu.Lock()
	runCalls := ag.runCalls
	ag.mu.Unlock()
	if countActiveTasks(h) != 0 || runCalls != 0 {
		t.Fatalf("接管未提交时任务越过 binding：active=%d run=%d", countActiveTasks(h), runCalls)
	}
	closeTestChannel(handoffRelease)
	waitDone(t, switchDone, "接管完成")
	waitDone(t, messageDone, "普通消息完成准入")
	waitDone(t, ag.turnEntered, "普通消息启动 B turn")
	task := assertAdmittedTaskUsesTarget(t, admittedTaskExpectation{
		h: h, ag: ag, workspaceA: workspaceA, workspaceB: workspaceB,
	})
	closeTestChannel(turnRelease)
	waitDone(t, task.done, "B task 清理")
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

func countActiveTasks(h *Handler) int {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	return len(h.activeTasks)
}

func countRemoteCodexOwners(store *codexSessionStore) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for _, intent := range store.controls {
		if intent.Owner == codexControlRemote {
			count++
		}
	}
	return count
}
