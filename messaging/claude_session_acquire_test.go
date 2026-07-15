package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestAcquireClaudeSessionReleasesAAndOwnsB(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1}
	persistCalls := 0
	store.persist = func(claudeSessionState) error { persistCalls++; return nil }
	fake.sessionID = "session-a"
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Control.Owner != claudeOwnerRemote || store.binding(key).SessionID != "session-b" {
		t.Fatalf("result=%+v binding=%+v", result, store.binding(key))
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("old=%+v", got)
	}
	if persistCalls != 1 {
		t.Fatalf("persistCalls=%d, want one atomic commit", persistCalls)
	}
}

func TestAcquireClaudeSessionRejectsOtherRemoteRouteBeforeResume(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	ownerKey := claudeBindingKey("owner", "claude")
	h.ensureClaudeSessions().controls["session-b"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: ownerKey, ConversationID: "owner-conversation", Revision: 1}
	route := newClaudeAcquireRoute(context.Background(), "request", "request", "claude", fake, workspace)

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if !errors.Is(err, errClaudeRemoteSelectionOtherRoute) || len(fake.useCalls) != 0 {
		t.Fatalf("error=%v useCalls=%+v", err, fake.useCalls)
	}
}

func TestAcquireClaudeSessionRejectsActiveOldBeforeResume(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	oldConversation := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: oldConversation, Revision: 1}
	if _, _, started := h.beginActiveTask(context.Background(), oldConversation, activeTaskMeta{owner: "user-1", agentName: "claude"}); !started {
		t.Fatal("failed to create active task")
	}
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if !errors.Is(err, errClaudeSessionAcquireActiveOld) || len(fake.useCalls) != 0 {
		t.Fatalf("error=%v useCalls=%+v", err, fake.useCalls)
	}
}

func TestAcquireClaudeSessionSameOwnerIsIdempotent(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 7}
	fake.sessionID = "session-a"
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-a", Cwd: workspace}, Command: "switch"})
	if err != nil || len(fake.useCalls) != 0 || result.Control.Revision != 7 {
		t.Fatalf("error=%v calls=%v result=%+v", err, fake.useCalls, result)
	}
}

func TestAcquireClaudeSessionStoreFailureRestoresOldRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	fake.sessionID = "session-a"
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if err == nil || fake.sessionID != "session-a" || store.binding(key).SessionID != "session-a" {
		t.Fatalf("error=%v runtime=%q binding=%+v", err, fake.sessionID, store.binding(key))
	}
}

func TestAcquireClaudeSessionRuntimeRollbackFailureFailsClosed(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1}
	fake.sessionID = "session-a"
	fake.useErr = errors.New("runtime unavailable")
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if !errors.Is(err, errClaudeSessionAcquireUncertain) {
		t.Fatalf("error=%v, want uncertain", err)
	}
	if old := store.controlIntent("session-a"); old.Owner == claudeOwnerRemote {
		t.Fatalf("old=%+v, uncertain runtime must fail closed", old)
	}
}

func TestAcquireClaudeSessionAgentSelectionFailureCompensatesStoreAndRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1}
	fake.sessionID = "session-a"
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

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if err == nil || store.binding(key).SessionID != "session-a" || store.controlIntent("session-a").Owner != claudeOwnerRemote || fake.sessionID != "session-a" {
		t.Fatalf("error=%v binding=%+v control=%+v runtime=%q", err, store.binding(key), store.controlIntent("session-a"), fake.sessionID)
	}
}

func TestAcquireClaudeSessionCompensationFailureFailsClosed(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1}
	fake.sessionID = "session-a"
	persistCalls := 0
	store.persist = func(claudeSessionState) error {
		persistCalls++
		if persistCalls == 2 {
			return errors.New("rollback disk failure")
		}
		return nil
	}
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

	_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch"})
	if !errors.Is(err, errClaudeSessionAcquireUncertain) {
		t.Fatalf("error=%v, want uncertain", err)
	}
	if target := store.controlIntent("session-b"); target.Owner == claudeOwnerRemote {
		t.Fatalf("target=%+v, compensation failure must fail closed", target)
	}
}

func TestAcquireClaudeSessionClearsReleasedRuntimeMapping(t *testing.T) {
	h, fake, workspaceA := newClaudeACPNavigationHandler(t)
	workspaceB := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspaceA, workspaceB})
	key := claudeBindingKey("user-1", "claude")
	oldConversation := buildClaudeConversationID("user-1", "claude", workspaceA)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspaceA, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: key, ConversationID: oldConversation, Revision: 1}
	fake.sessionID = "session-a"
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspaceA)

	result, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspaceB}, Command: "switch"})
	current, ok := fake.CurrentClaudeSession(result.ConversationID)
	if err != nil || fake.clearCalledWith != oldConversation || !ok || current != "session-b" {
		t.Fatalf("error=%v clear=%q current=(%q,%t) want=%q/session-b", err, fake.clearCalledWith, current, ok, oldConversation)
	}
}

func TestRenderClaudeSessionAcquireFailureDoesNotLeakRouteIdentity(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.Join(errClaudeRemoteSelectionOtherRoute, errors.New("route-user\x00claude owner-conversation")), "该 Claude 会话正由其他远程窗口控制，请先在原窗口释放控制权。"},
		{errors.Join(errClaudeSessionAcquireActiveOld, errors.New("route-user")), "当前窗口的 Claude 远程任务仍在执行，请等待任务结束或先发送 /stop。"},
		{errors.Join(errClaudeRemoteSelectionChanged, errors.New("route-user")), "Claude 会话状态刚刚发生变化，请重试。"},
		{errors.Join(errClaudeSessionAcquireUncertain, errors.New("route-user")), "Claude 控制权移交结果未确认，已停止继续操作；请检查状态后重试。"},
		{errors.New("disk full route-user"), "切换并接管 Claude 会话失败，请稍后重试。"},
	}
	for _, test := range tests {
		text := renderClaudeSessionAcquireFailure(test.err)
		if text != test.want || strings.Contains(text, "route-user") || strings.Contains(text, "owner-conversation") {
			t.Fatalf("error=%v text=%q want=%q", test.err, text, test.want)
		}
	}
}

func acquireClaudeSessionForTest(h *Handler, request claudeSessionAcquireRequest) (claudeSessionAcquireResult, error) {
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(request.Route.BindingKey))
	defer unlock()
	return h.acquireClaudeSessionWithBindingLocked(request)
}

func newClaudeAcquireRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, ag agent.Agent, workspaceRoot string) claudeSessionRoute {
	return claudeSessionRoute{Context: ctx, ActorUserID: actorUserID, UserID: routeUserID, AgentName: agentName, Agent: ag, WorkspaceRoot: workspaceRoot, BindingKey: claudeBindingKey(routeUserID, agentName)}
}
