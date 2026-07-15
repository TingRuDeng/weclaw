package messaging

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type integratedSelectionResult struct {
	index int
	texts []string
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
	before     []codexRouteSelectionSnapshot
}

type codexRouteSelectionSnapshot struct {
	binding    codexSessionBinding
	routeOwned map[string]codexControlIntent
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
	h.SetCodexLocalSessionDir(t.TempDir())
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
	before := make([]codexRouteSelectionSnapshot, len(routes))
	for index, route := range routes {
		snapshot := h.codexSessions.remoteSelectionSnapshot(codexBindingKey(route, "codex"), "thread-b")
		before[index] = codexRouteSelectionSnapshot{binding: snapshot.Binding, routeOwned: snapshot.RouteOwned}
	}
	return concurrentSelectionFixture{h: h, ag: ag, routes: routes, oldThreads: oldThreads, workspaces: workspaces, before: before}
}

func (f concurrentSelectionFixture) run(t *testing.T) []integratedSelectionResult {
	t.Helper()
	unlockHolder := f.h.lockCodexThreadControl("thread-b")
	var unlockOnce sync.Once
	releaseHolder := func() { unlockOnce.Do(unlockHolder) }
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		releaseHolder()
		wg.Wait()
	})
	start := make(chan struct{})
	resultCh := make(chan integratedSelectionResult, len(f.routes))
	for index, route := range f.routes {
		wg.Add(1)
		go func(index int, route string) {
			defer wg.Done()
			reply := platformtest.NewReplier(platform.Capabilities{Text: true})
			<-start
			f.h.HandleMessage(ctx, platform.IncomingMessage{
				Platform: platform.PlatformWeChat, AccountID: "wx-" + route,
				UserID: route, MessageID: "concurrent-select-" + route, Text: "/cx switch thread-b",
			}, reply)
			resultCh <- integratedSelectionResult{index: index, texts: append([]string(nil), reply.Texts...)}
		}(index, route)
	}
	close(start)
	waitForExecutionLockUsers(t, executionLockUsersExpectation{
		h: f.h, key: codexThreadControlExecutionPrefix + "thread-b", users: 3,
	})
	releaseHolder()
	got := make([]integratedSelectionResult, len(f.routes))
	for range f.routes {
		result := waitIntegratedSelectionResult(t, resultCh)
		got[result.index] = result
	}
	wg.Wait()
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
	loserSnapshot := f.h.codexSessions.remoteSelectionSnapshot(loserBinding, "thread-b")
	loserState := codexRouteSelectionSnapshot{binding: loserSnapshot.Binding, routeOwned: loserSnapshot.RouteOwned}
	if !reflect.DeepEqual(loserState, f.before[loser]) {
		t.Fatalf("失败方完整状态被修改：got=%#v want=%#v", loserState, f.before[loser])
	}
	if f.ag.threadBinding(f.oldThreads[loser]).Runtime != agent.CodexRuntimeWeClaw ||
		f.ag.threadBinding(f.oldThreads[winner]).Control.Owner != agent.CodexControlDesktop {
		t.Fatalf("runtime 状态与赢家不一致：winner=%#v loser=%#v", f.ag.threadBinding(f.oldThreads[winner]), f.ag.threadBinding(f.oldThreads[loser]))
	}
	loserText := got[loser].texts[0]
	if strings.Contains(loserText, f.routes[winner]) || strings.Contains(loserText, winnerBinding) ||
		strings.Contains(loserText, target.ConversationID) {
		t.Fatalf("失败回复泄露赢家身份：%q", loserText)
	}
	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(f.h.codexSessions.filePath)
	persisted := reloaded.controlIntent("thread-b")
	if persisted.RouteBindingKey != winnerBinding || persisted.Revision != target.Revision {
		t.Fatalf("persisted target=%#v，want route=%q revision=%d", persisted, winnerBinding, target.Revision)
	}
	assertRuntimeTargetMatchesPersisted(t, f.ag.threadBinding("thread-b"), persisted)
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
	for _, result := range results {
		if len(result.texts) != 1 {
			t.Fatalf("route %d 回复数量=%d，want 1：%#v", result.index, len(result.texts), result.texts)
		}
	}
	success := []bool{strings.HasPrefix(results[0].texts[0], "已切换并接管。"), strings.HasPrefix(results[1].texts[0], "已切换并接管。")}
	if success[0] == success[1] {
		t.Fatalf("必须恰好一个成功：results=%#v", results)
	}
	if success[0] {
		if !strings.Contains(results[1].texts[0], "其他远程窗口") {
			t.Fatalf("失败回复=%q", results[1].texts[0])
		}
		return integratedSelectionOutcome{winner: 0, loser: 1}
	}
	if !strings.Contains(results[0].texts[0], "其他远程窗口") {
		t.Fatalf("失败回复=%q", results[0].texts[0])
	}
	return integratedSelectionOutcome{winner: 1, loser: 0}
}

func assertRuntimeTargetMatchesPersisted(t *testing.T, runtime agent.CodexThreadBinding, persisted codexControlIntent) {
	t.Helper()
	control := runtime.Control
	if runtime.Ref.ThreadID != "thread-b" || runtime.Ref.ConversationID != persisted.ConversationID ||
		control.Owner != agent.CodexControlRemote || control.RouteKey != persisted.RouteBindingKey ||
		control.ConversationID != persisted.ConversationID || control.Revision != persisted.Revision {
		t.Fatalf("runtime B=%#v persisted=%#v", runtime, persisted)
	}
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
