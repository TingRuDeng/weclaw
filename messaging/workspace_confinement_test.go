package messaging

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexWorkspaceGroupsIncludeAllVisibleWorkspaces(t *testing.T) {
	h := NewHandler(nil, nil)
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	mustCreateWorkspaceDirs(t, workspaceA, workspaceB)
	codexDir := t.TempDir()
	writeCodexAppWorkspaceState(t, codexDir, []string{workspaceA, workspaceB}, []string{workspaceA, workspaceB})
	h.SetCodexLocalSessionDir(codexDir)

	groups, err := h.codexWorkspaceGroupsForUser(codexBindingKey("user-1", "codex"), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups=%#v, want both visible workspaces", groups)
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
