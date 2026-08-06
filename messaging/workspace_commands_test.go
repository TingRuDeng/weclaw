package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexWorkspaceAddRequiresAdminPrivateAndPreservesSpaces(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "project with spaces")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newWorkspaceCommandTestHandler(t, "codex")
	command := "/cx workspace add " + workspace

	ordinary := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: command, Private: true,
	})
	if !strings.Contains(ordinary, "仅管理员") {
		t.Fatalf("ordinary reply=%q", ordinary)
	}
	group := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "group:1", Trimmed: command, Admin: true, Private: false,
	})
	if !strings.Contains(group, "私聊") {
		t.Fatalf("group reply=%q", group)
	}
	success := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: command,
		Platform: platform.PlatformFeishu, Admin: true, Private: true,
	})
	if !strings.Contains(success, "已登记 Codex 工作空间") || !strings.Contains(success, workspace) {
		t.Fatalf("success reply=%q", success)
	}
	snapshot, err := h.ensureWorkspaceRegistry().Snapshot("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Registered) != 1 || snapshot.Registered[0].Root != canonicalTestWorkspace(t, workspace) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestClaudeWorkspaceAddUsesSameAdminPrivateBoundary(t *testing.T) {
	workspace := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "claude")
	req := claudeSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1",
		Trimmed:  "/cc workspace add " + workspace,
		Platform: platform.PlatformWeChat, Admin: true, Private: true,
	}

	result := h.handleClaudeSessionCommandForRouteRequest(context.Background(), req).Reply
	if !strings.Contains(result, "已登记 Claude 工作空间") {
		t.Fatalf("reply=%q", result)
	}
	snapshot, err := h.ensureWorkspaceRegistry().Snapshot("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Registered) != 1 || snapshot.Registered[0].Root != canonicalTestWorkspace(t, workspace) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestWorkspaceRegistryOverlayMergesCodexAndClaudeCatalogs(t *testing.T) {
	registered := t.TempDir()
	hidden := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "codex")
	if _, err := h.ensureWorkspaceRegistry().Add("codex", registered); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ensureWorkspaceRegistry().Remove("codex", hidden); err != nil {
		t.Fatal(err)
	}
	codexDir := t.TempDir()
	writeCodexAppWorkspaceState(t, codexDir, []string{hidden}, []string{hidden})
	h.SetCodexLocalSessionDir(codexDir)

	codexGroups, err := h.codexWorkspaceListForAccess(codexBindingKey("admin-1", "codex"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(codexGroups) != 1 || codexGroups[0].Root != canonicalTestWorkspace(t, registered) {
		t.Fatalf("codex groups=%+v, want registered root only", codexGroups)
	}

	claude := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}}
	claude.catalogSessions = []agent.ClaudeSession{{ID: "hidden-session", Cwd: hidden}}
	h.defaultName = "claude"
	h.agents["claude"] = claude
	if _, err := h.ensureWorkspaceRegistry().Add("claude", registered); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ensureWorkspaceRegistry().Remove("claude", hidden); err != nil {
		t.Fatal(err)
	}
	claudeGroups, err := h.claudeWorkspaceGroupsForRoute(claudeSessionRoute{
		Context: context.Background(), ActorUserID: "admin-1", UserID: "admin-1",
		AgentName: "claude", Agent: claude, BindingKey: claudeBindingKey("admin-1", "claude"), Admin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claudeGroups) != 1 || claudeGroups[0].Root != canonicalTestWorkspace(t, registered) {
		t.Fatalf("claude groups=%+v, want registered root only", claudeGroups)
	}
}

func TestRegisteredWorkspaceDoesNotExpandOrdinaryUserAccess(t *testing.T) {
	allowed := t.TempDir()
	registeredOutside := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "codex")
	h.SetAllowedWorkspaceRoots([]string{allowed})
	if _, err := h.ensureWorkspaceRegistry().Add("codex", registeredOutside); err != nil {
		t.Fatal(err)
	}

	groups, err := h.codexWorkspaceListForAccess(codexBindingKey("user-1", "codex"), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group.Root == registeredOutside {
			t.Fatalf("ordinary user saw admin-registered outside root: %+v", groups)
		}
	}
}

func TestWorkspaceRemoveRejectsActiveFrontendBinding(t *testing.T) {
	workspace := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "codex")
	if _, err := h.ensureWorkspaceRegistry().Add("codex", workspace); err != nil {
		t.Fatal(err)
	}
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey("user-1", "codex"), workspace)

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: "/cx workspace remove " + workspace,
		Admin: true, Private: true,
	})
	if !strings.Contains(reply, "仍被窗口使用") {
		t.Fatalf("reply=%q", reply)
	}
	snapshot, err := h.ensureWorkspaceRegistry().Snapshot("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Registered) != 1 || snapshot.IsHidden(workspace) {
		t.Fatalf("active removal changed registry: %+v", snapshot)
	}
}

func TestHiddenCodexWorkspaceRejectsDirectThreadSelection(t *testing.T) {
	workspace := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "codex")
	if _, err := h.ensureWorkspaceRegistry().Remove("codex", workspace); err != nil {
		t.Fatal(err)
	}
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-hidden")

	_, _, err := h.resolveCodexSwitchTarget(codexSwitchTargetRequest{
		bindingKey: bindingKey, agentName: "codex", workspaceRoot: workspace,
		target: "thread-hidden", agent: &fakeCodexThreadAgent{},
	})
	if err == nil || !strings.Contains(err.Error(), "已被管理员移除") {
		t.Fatalf("error=%v, want hidden workspace rejection", err)
	}
}

func TestHiddenWorkspaceBlocksExistingCodexAndClaudeBindings(t *testing.T) {
	workspace := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "codex")
	if _, err := h.ensureWorkspaceRegistry().Remove("codex", workspace); err != nil {
		t.Fatal(err)
	}
	codex := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}}}
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-hidden")
	_, codexErr := h.resolveCodexConversationIDForRoute(context.Background(), "user-1", "user-1", "codex", codex)
	if codexErr == nil || !strings.Contains(codexErr.Error(), "已被管理员移除") {
		t.Fatalf("Codex error=%v, want hidden workspace rejection", codexErr)
	}

	claude := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}}
	h.agents["claude"] = claude
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	if _, err := h.ensureWorkspaceRegistry().Remove("claude", workspace); err != nil {
		t.Fatal(err)
	}
	claudeKey := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(claudeKey, workspace, "session-hidden"); err != nil {
		t.Fatal(err)
	}
	_, claudeErr := h.resolveClaudeConversationIDForRoute(context.Background(), "user-1", "user-1", "claude", claude)
	if claudeErr == nil || !strings.Contains(claudeErr.Error(), "已被管理员移除") {
		t.Fatalf("Claude error=%v, want hidden workspace rejection", claudeErr)
	}
}

func TestCwdRejectsWorkspaceHiddenForConfiguredAgent(t *testing.T) {
	workspace := t.TempDir()
	h := newWorkspaceCommandTestHandler(t, "codex")
	codex := &fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}}
	h.agents["codex"] = codex
	h.SetAllowedWorkspaceRoots([]string{workspace})
	if _, err := h.ensureWorkspaceRegistry().Remove("codex", workspace); err != nil {
		t.Fatal(err)
	}

	reply := h.handleCwdWithAccess("/cwd "+workspace, []string{"admin-1"}, true)
	if !strings.Contains(reply, "已被管理员从 WeClaw 移除") {
		t.Fatalf("reply=%q, want hidden workspace rejection", reply)
	}
	if got := codex.lastWorkingDir(); got != "" {
		t.Fatalf("hidden /cwd changed agent cwd to %q", got)
	}
	if _, ok := h.ensureCodexSessions().getActiveWorkspace(codexBindingKey("admin-1", "codex")); ok {
		t.Fatal("hidden /cwd committed Codex active workspace")
	}
}

func newWorkspaceCommandTestHandler(t *testing.T, agentName string) *Handler {
	t.Helper()
	h := NewHandler(nil, nil)
	if err := h.SetWorkspaceRegistryFile(filepath.Join(t.TempDir(), "workspace-registry.json")); err != nil {
		t.Fatalf("SetWorkspaceRegistryFile: %v", err)
	}
	command := agentName
	if agentName == "claude" {
		command = "claude-agent-acp"
	}
	h.SetAgentMetas([]AgentMeta{{Name: agentName, Type: "acp", Command: command}})
	return h
}

func canonicalTestWorkspace(t *testing.T, root string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve test workspace: %v", err)
	}
	return filepath.Clean(canonical)
}
