package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestCodexSessionRemoveRequiresAdminPrivateAndCanRestoreByID(t *testing.T) {
	h := newWorkspaceCommandTestHandler(t, "codex")
	codexDir := t.TempDir()
	workspace := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-hide", workspace, "待隐藏会话", "2026-08-07T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	command := "/cx session remove thread-hide"

	ordinary := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: command, Private: true,
	})
	if !strings.Contains(ordinary, "当前账号未授权") {
		t.Fatalf("ordinary=%q", ordinary)
	}
	group := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "group:1", Trimmed: command, Admin: true, Private: false,
	})
	if !strings.Contains(group, "私聊") {
		t.Fatalf("group=%q", group)
	}
	success := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: command,
		Platform: platform.PlatformFeishu, Admin: true, Private: true,
	})
	if !strings.Contains(success, "已从 WeClaw 导航隐藏 Codex 会话") || !strings.Contains(success, "/cx session restore thread-hide") {
		t.Fatalf("success=%q", success)
	}
	if views := h.codexSwitchTargets(codexBindingKey("admin-1", "codex")); len(views) != 0 {
		t.Fatalf("hidden session still visible: %+v", views)
	}
	if _, _, err := h.resolveCodexSwitchTarget(codexSwitchTargetRequest{
		bindingKey: codexBindingKey("admin-1", "codex"), agentName: "codex",
		workspaceRoot: workspace, target: "thread-hide", agent: &fakeCodexThreadAgent{},
	}); err == nil || !strings.Contains(err.Error(), "已从 WeClaw 导航隐藏") {
		t.Fatalf("direct hidden switch error=%v", err)
	}

	restored := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: "/cx session restore thread-hide",
		Admin: true, Private: true,
	})
	if !strings.Contains(restored, "已恢复 Codex 会话") || len(h.codexSwitchTargets(codexBindingKey("admin-1", "codex"))) != 1 {
		t.Fatalf("restored=%q views=%+v", restored, h.codexSwitchTargets(codexBindingKey("admin-1", "codex")))
	}
}

func TestClaudeSessionRemoveFiltersListDirectIDAndStaleChoice(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	if err := h.SetWorkspaceRegistryFile(filepath.Join(t.TempDir(), "workspace-registry.json")); err != nil {
		t.Fatal(err)
	}
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-hide", Cwd: workspace, Title: "待隐藏会话"}}
	req := claudeSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: "/cc session remove 1",
		Platform: platform.PlatformFeishu, Admin: true, Private: true,
	}
	result := h.handleClaudeSessionCommandForRouteRequest(context.Background(), req).Reply
	if !strings.Contains(result, "已从 WeClaw 导航隐藏 Claude 会话") || !strings.Contains(result, "/cc session restore session-hide") {
		t.Fatalf("result=%q", result)
	}
	route := claudeSessionRoute{
		Context: context.Background(), ActorUserID: "admin-1", UserID: "admin-1",
		AgentName: "claude", Agent: fake, WorkspaceRoot: workspace,
		BindingKey: claudeBindingKey("admin-1", "claude"), Admin: true,
	}
	if views, err := h.claudeSwitchTargets(route); err != nil || len(views) != 0 {
		t.Fatalf("views=%+v err=%v", views, err)
	}
	if _, err := h.findClaudeSessionForRoute(route, "session-hide"); err == nil {
		t.Fatal("stale direct session choice bypassed hidden overlay")
	}
	restored := h.handleClaudeSessionCommandForRouteRequest(context.Background(), claudeSessionCommandRequest{
		ActorUserID: "admin-1", RouteUserID: "admin-1", Trimmed: "/cc session restore session-hide",
		Admin: true, Private: true,
	}).Reply
	if !strings.Contains(restored, "已恢复 Claude 会话") {
		t.Fatalf("restored=%q", restored)
	}
}

func TestSessionRemoveRejectsAnyBoundOrNonterminalSession(t *testing.T) {
	t.Run("codex bound", func(t *testing.T) {
		h := newWorkspaceCommandTestHandler(t, "codex")
		workspace := t.TempDir()
		dir := t.TempDir()
		writeLocalCodexSession(t, dir, "thread-bound", workspace, "绑定会话", "2026-08-07T08:00:00Z")
		h.SetCodexLocalSessionDir(dir)
		h.ensureCodexSessions().setThread(codexBindingKey("other", "codex"), workspace, "thread-bound")
		reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
			ActorUserID: "admin", RouteUserID: "admin", Trimmed: "/cx session remove thread-bound",
			Admin: true, Private: true,
		})
		if !strings.Contains(reply, "仍被窗口绑定") {
			t.Fatalf("reply=%q", reply)
		}
	})

	t.Run("claude task", func(t *testing.T) {
		h, fake, workspace := newClaudeACPNavigationHandler(t)
		if err := h.SetWorkspaceRegistryFile(filepath.Join(t.TempDir(), "workspace-registry.json")); err != nil {
			t.Fatal(err)
		}
		fake.catalogSessions = []agent.ClaudeSession{{ID: "session-running", Cwd: workspace}}
		task, _, started := h.beginActiveTask(context.Background(), claudeSessionExecutionKey("session-running"), activeTaskMeta{
			owner: "user", routeUserID: "user", agentName: "claude", sessionID: "session-running",
		})
		if !started {
			t.Fatal("task did not start")
		}
		defer h.finishActiveTask(claudeSessionExecutionKey("session-running"), task)
		reply := h.handleClaudeSessionCommandForRouteRequest(context.Background(), claudeSessionCommandRequest{
			ActorUserID: "admin", RouteUserID: "admin", Trimmed: "/cc session remove session-running",
			Admin: true, Private: true,
		}).Reply
		if !strings.Contains(reply, "任务运行或状态未确认") {
			t.Fatalf("reply=%q", reply)
		}
	})
}
