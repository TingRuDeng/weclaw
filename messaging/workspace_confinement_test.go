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

func TestCodexWorkspaceGroupsRespectAllowedRootsForOrdinaryUser(t *testing.T) {
	h := NewHandler(nil, nil)
	allowed := filepath.Join(t.TempDir(), "allowed")
	blocked := filepath.Join(t.TempDir(), "blocked")
	mustCreateWorkspaceDirs(t, allowed, blocked)
	codexDir := t.TempDir()
	writeCodexAppWorkspaceState(t, codexDir, []string{allowed, blocked}, []string{allowed, blocked})
	h.SetCodexLocalSessionDir(codexDir)
	h.SetAllowedWorkspaceRoots([]string{allowed})

	groups := h.codexWorkspaceGroupsForUser(codexBindingKey("user-1", "codex"), "user-1")
	if len(groups) != 1 || groups[0].Root != normalizeCodexWorkspaceRoot(allowed) {
		t.Fatalf("groups=%#v, want only allowed workspace", groups)
	}
}

func TestCodexWorkspaceGroupsBypassAllowedRootsForAdmin(t *testing.T) {
	h := NewHandler(nil, nil)
	allowed := filepath.Join(t.TempDir(), "allowed")
	blocked := filepath.Join(t.TempDir(), "blocked")
	mustCreateWorkspaceDirs(t, allowed, blocked)
	codexDir := t.TempDir()
	writeCodexAppWorkspaceState(t, codexDir, []string{allowed, blocked}, []string{allowed, blocked})
	h.SetCodexLocalSessionDir(codexDir)
	h.SetAllowedWorkspaceRoots([]string{allowed})
	h.SetAdminUsers([]string{"admin-1"})

	groups := h.codexWorkspaceGroupsForUser(codexBindingKey("admin-1", "codex"), "admin-1")
	if len(groups) != 2 {
		t.Fatalf("groups=%#v, want admin to see both workspaces", groups)
	}
}

func mustCreateWorkspaceDirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
}

func TestClaudeConversationUsesRouteWorkspaceWithoutChangingGlobalCwd(t *testing.T) {
	h := NewHandler(nil, nil)
	globalRoot := filepath.Join(t.TempDir(), "global")
	routeRoot := filepath.Join(t.TempDir(), "route")
	mustCreateWorkspaceDirs(t, globalRoot, routeRoot)
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli"}}}
	h.SetAgentWorkDirs(map[string]string{"claude": globalRoot})
	h.SetAllowedWorkspaceRoots([]string{routeRoot})
	bindingKey := claudeBindingKey("route-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(bindingKey, routeRoot, "session-route"); err != nil {
		t.Fatal(err)
	}

	conversationID, err := h.resolveClaudeConversationIDForRoute(context.Background(), "actor-1", "route-1", "claude", ag)
	if err != nil {
		t.Fatalf("resolve conversation: %v", err)
	}
	if ag.conversationCwds[conversationID] != normalizeClaudeWorkspaceRoot(routeRoot) {
		t.Fatalf("conversation cwd=%q, want %q", ag.conversationCwds[conversationID], normalizeClaudeWorkspaceRoot(routeRoot))
	}
	if h.agentWorkDirs["claude"] != globalRoot || ag.lastWorkingDir() != "" {
		t.Fatalf("global cwd mutated: handler=%q agent=%q", h.agentWorkDirs["claude"], ag.lastWorkingDir())
	}
}

func TestCodexCommandRejectsStaleWorkspaceForOrdinaryUser(t *testing.T) {
	h := NewHandler(nil, nil)
	allowed := filepath.Join(t.TempDir(), "allowed")
	blocked := filepath.Join(t.TempDir(), "blocked")
	mustCreateWorkspaceDirs(t, allowed, blocked)
	h.SetAllowedWorkspaceRoots([]string{allowed})
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey("user-1", "codex"), blocked)
	opened := false
	h.SetCodexAppOpener(func(context.Context, string, string) error {
		opened = true
		return nil
	})

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx app",
	})

	if opened || !strings.Contains(reply, "不在允许范围") {
		t.Fatalf("opened=%v reply=%q, want confinement rejection", opened, reply)
	}
}

func TestCodexCommandAllowsStaleWorkspaceForAdmin(t *testing.T) {
	h := NewHandler(nil, nil)
	blocked := filepath.Join(t.TempDir(), "blocked")
	mustCreateWorkspaceDirs(t, blocked)
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey("admin-1", "codex"), blocked)
	opened := false
	h.SetCodexAppOpener(func(context.Context, string, string) error {
		opened = true
		return nil
	})

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: "/cx app", Admin: true,
	})

	if !opened || strings.Contains(reply, "不在允许范围") {
		t.Fatalf("opened=%v reply=%q, want admin bypass", opened, reply)
	}
}

func TestCodexOwnerRemoteRejectsStaleWorkspaceForOrdinaryUser(t *testing.T) {
	h := NewHandler(nil, nil)
	allowed := filepath.Join(t.TempDir(), "allowed")
	blocked := filepath.Join(t.TempDir(), "blocked")
	mustCreateWorkspaceDirs(t, allowed, blocked)
	h.SetAllowedWorkspaceRoots([]string{allowed})
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{ThreadID: "thread-1"})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, blocked)
	h.ensureCodexSessions().setThread(bindingKey, blocked, "thread-1")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx owner remote",
	})

	if ag.handoffCalls != 0 || !strings.Contains(reply, "不在允许范围") {
		t.Fatalf("handoff=%d reply=%q，普通用户不应接管受限工作空间", ag.handoffCalls, reply)
	}
}

func TestCodexMessageRejectsStaleWorkspaceForOrdinaryUser(t *testing.T) {
	h := NewHandler(nil, nil)
	allowed := filepath.Join(t.TempDir(), "allowed")
	blocked := filepath.Join(t.TempDir(), "blocked")
	mustCreateWorkspaceDirs(t, allowed, blocked)
	h.SetAllowedWorkspaceRoots([]string{allowed})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		reply: "不应执行",
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	h.SetDefaultAgent("codex", ag)
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey("user-1", "codex"), blocked)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformWeChat, UserID: "user-1", Text: "执行任务",
	}, reply)
	waitUntil(t, func() bool { return len(reply.Texts) > 0 || ag.lastChatMessage() != "" })

	if got := ag.lastChatMessage(); got != "" {
		t.Fatalf("agent received disallowed message=%q", got)
	}
	if !containsText(reply.Texts, "不在允许范围") {
		t.Fatalf("reply texts=%#v, want confinement rejection", reply.Texts)
	}
	time.Sleep(10 * time.Millisecond)
}
