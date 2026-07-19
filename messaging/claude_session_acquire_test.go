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
)

func TestAcquireClaudeSessionChangesFrontendBinding(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 3)
	persistCalls := 0
	h.ensureClaudeSessions().persist = func(claudeSessionState) error { persistCalls++; return nil }
	fake.sessionID = "session-a"
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Binding.SessionID != "session-b" || h.ensureClaudeSessions().binding(key).SessionID != "session-b" {
		t.Fatalf("result=%+v binding=%+v", result, h.ensureClaudeSessions().binding(key))
	}
	if persistCalls != 2 {
		t.Fatalf("persistCalls=%d, want binding then ready", persistCalls)
	}
}

func TestAcquireClaudeSessionAllowsAnotherRouteOnSameTarget(t *testing.T) {
	h, _, workspace := newClaudeACPNavigationHandler(t)
	first := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}}
	second := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}}
	for _, route := range []claudeSessionRoute{
		newClaudeAcquireRoute(context.Background(), "a", "route-a", "claude", first, workspace),
		newClaudeAcquireRoute(context.Background(), "b", "route-b", "claude", second, workspace),
	} {
		if _, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
			Route: route, Selected: agent.ClaudeSession{ID: "session-shared", Cwd: workspace}, Command: "switch",
		}); err != nil {
			t.Fatalf("route=%s: %v", route.UserID, err)
		}
	}
}

func TestAcquireClaudeSessionRejectsCallingRouteActiveOldSession(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 1)
	taskKey := claudeSessionExecutionKey("session-a")
	task, _, started := h.beginActiveTask(context.Background(), taskKey, activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "claude",
	})
	if !started {
		t.Fatal("failed to create active task")
	}
	defer h.finishActiveTask(taskKey, task)
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch",
	})
	if !errors.Is(err, errClaudeSessionAcquireActiveOld) || h.ensureClaudeSessions().binding(key).SessionID != "session-a" {
		t.Fatalf("err=%v binding=%+v", err, h.ensureClaudeSessions().binding(key))
	}
}

func TestAcquireClaudeSessionSameBindingIsIdempotent(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 7)
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	fake.runtimeSessions = map[string]string{conversationID: "session-a"}
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-a", Cwd: workspace}, Command: "switch",
	})
	if err != nil || len(fake.useCalls) != 0 || result.Binding.Revision != 7 || h.ensureClaudeSessions().binding(key).Revision != 7 {
		t.Fatalf("err=%v useCalls=%v result=%+v", err, fake.useCalls, result)
	}
}

func TestAcquireClaudeSessionRuntimeFailureKeepsBindingFailClosed(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 1)
	fake.sessionID = "session-a"
	fake.useErr = errors.New("resume failed")
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch",
	})
	if err != nil || result.RuntimeErr == nil {
		t.Fatalf("err=%v result=%+v", err, result)
	}
	binding := h.ensureClaudeSessions().binding(key)
	if binding.SessionID != "session-b" || binding.Status != claudeBindingResumeFailed {
		t.Fatalf("binding=%+v", binding)
	}
	if _, writeErr := h.ensureClaudeSessions().requireWritableBinding(key); !errors.Is(writeErr, errClaudeRuntimeUnavailable) {
		t.Fatalf("writeErr=%v", writeErr)
	}
}

func TestAcquireClaudeSessionReadyPersistenceFailureKeepsResumeFailed(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 1)
	persistCalls := 0
	h.ensureClaudeSessions().persist = func(claudeSessionState) error {
		persistCalls++
		if persistCalls == 2 {
			return errors.New("disk full")
		}
		return nil
	}
	fake.sessionID = "session-a"
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch",
	})
	if err != nil || result.RuntimeErr == nil {
		t.Fatalf("err=%v result=%+v", err, result)
	}
	if binding := h.ensureClaudeSessions().binding(key); binding.SessionID != "session-b" || binding.Status != claudeBindingResumeFailed {
		t.Fatalf("binding=%+v", binding)
	}
}

func TestAcquireClaudeSessionAgentSelectionFailureRollsBackBinding(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 1)
	statePath := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := h.SetAgentSessionFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := h.ensureAgentSessions().Set("user-1", "codex"); err != nil {
		t.Fatal(err)
	}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.ensureAgentSessions().filePath = filepath.Join(invalidParent, "state.json")
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch",
	})
	if err == nil || h.ensureClaudeSessions().binding(key).SessionID != "session-a" {
		t.Fatalf("err=%v binding=%+v", err, h.ensureClaudeSessions().binding(key))
	}
}

func TestForceClaudeBindingFailClosedObeysLockOrder(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("user-1", "claude")
	store.bindings[key] = newClaudeBinding("/workspace", "session-a", claudeBindingReady)
	store.saveMu.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_ = forceClaudeBindingFailClosedInMemory(store, key)
		close(done)
	}()
	<-started
	select {
	case <-done:
		store.saveMu.Unlock()
		t.Fatal("fail-closed bypassed saveMu")
	case <-time.After(30 * time.Millisecond):
	}
	store.saveMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fail-closed did not resume")
	}
	if got := store.binding(key); got.Status != claudeBindingResumeFailed {
		t.Fatalf("binding=%+v", got)
	}
}

func TestAcquireClaudeSessionClearsOnlyPreviousRouteRuntimeMapping(t *testing.T) {
	h, fake, workspaceA := newClaudeACPNavigationHandler(t)
	workspaceB := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspaceA, workspaceB})
	seedClaudeBinding(t, h, "user-1", "claude", workspaceA, "session-a", 1)
	oldConversation := buildClaudeConversationID("user-1", "claude", workspaceA)
	fake.runtimeSessions = map[string]string{oldConversation: "session-a"}
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspaceA)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspaceB}, Command: "switch",
	})
	current, ok := fake.CurrentClaudeSession(result.ConversationID)
	if err != nil || fake.clearCalledWith != oldConversation || !ok || current != "session-b" {
		t.Fatalf("err=%v clear=%q current=(%q,%t)", err, fake.clearCalledWith, current, ok)
	}
}

func TestRenderClaudeSessionAcquireFailureDoesNotLeakRouteIdentity(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.Join(errClaudeSessionAcquireActiveOld, errors.New("route-user")), "当前窗口的 Claude 任务仍在执行，请等待任务结束或先发送 /stop。"},
		{errors.Join(errClaudeBindingSelectionChanged, errors.New("route-user")), "Claude 会话绑定刚刚发生变化，请重试。"},
		{errors.Join(errClaudeSessionAcquireUncertain, errors.New("route-user")), "Claude 会话绑定结果未确认，已停止继续操作；请检查状态后重试。"},
		{errors.New("disk full route-user"), "切换 Claude 会话失败，请稍后重试。"},
	}
	for _, test := range tests {
		text := renderClaudeSessionAcquireFailure(test.err)
		if text != test.want || strings.Contains(text, "route-user") {
			t.Fatalf("err=%v text=%q want=%q", test.err, text, test.want)
		}
	}
}

func acquireClaudeSessionForTest(h *Handler, request claudeSessionAcquireRequest) (claudeSessionAcquireResult, error) {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(request.Route.BindingKey))
	defer unlock()
	return h.acquireClaudeSessionWithBindingLocked(request)
}

func newClaudeAcquireRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, ag agent.Agent, workspaceRoot string) claudeSessionRoute {
	return claudeSessionRoute{
		Context: ctx, ActorUserID: actorUserID, UserID: routeUserID,
		AgentName: agentName, Agent: ag, WorkspaceRoot: workspaceRoot,
		BindingKey: claudeBindingKey(routeUserID, agentName),
	}
}
