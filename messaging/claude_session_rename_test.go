package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type fakeClaudeRenameAgent struct {
	*fakeClaudeSessionAgent
	mu          sync.Mutex
	renameCalls []claudeRenameCall
	renameErr   error
}

type claudeRenameCall struct {
	sessionID string
	name      string
}

func (f *fakeClaudeRenameAgent) RenameClaudeSession(_ context.Context, sessionID string, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameCalls = append(f.renameCalls, claudeRenameCall{sessionID: sessionID, name: name})
	return f.renameErr
}

func (f *fakeClaudeRenameAgent) calls() []claudeRenameCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]claudeRenameCall(nil), f.renameCalls...)
}

func TestClaudeRenameCurrentPreservesNameSpacesAndBindings(t *testing.T) {
	h, ag, workspace, bindingKey := newClaudeRenameCommandFixture(t, "session-current")
	otherKey := claudeBindingKey("user-2", "claude")
	if err := h.ensureClaudeSessions().commitSelection(otherKey, workspace, "session-current"); err != nil {
		t.Fatal(err)
	}

	reply := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc rename current 新 的 名称")

	if calls := ag.calls(); len(calls) != 1 || calls[0] != (claudeRenameCall{sessionID: "session-current", name: "新 的 名称"}) {
		t.Fatalf("rename calls=%#v", calls)
	}
	for _, key := range []string{bindingKey, otherKey} {
		binding := h.ensureClaudeSessions().binding(key)
		if binding.SessionID != "session-current" || binding.WorkspaceRoot != normalizeClaudeWorkspaceRoot(workspace) {
			t.Fatalf("binding %q changed: %+v", key, binding)
		}
	}
	if !strings.Contains(reply, "已重命名 Claude 会话") || strings.Contains(reply, "新 的 名称") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestClaudeRenameListIndexUsesVisibleCatalog(t *testing.T) {
	h, ag, workspace, _ := newClaudeRenameCommandFixture(t, "session-current")
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-newer", Cwd: workspace, Title: "较新", UpdatedAt: "2026-08-06T10:00:00Z"},
		{ID: "session-current", Cwd: workspace, Title: "当前", UpdatedAt: "2026-08-06T09:00:00Z"},
	}

	reply := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc rename 0 列表 新名称")

	if calls := ag.calls(); len(calls) != 1 || calls[0] != (claudeRenameCall{sessionID: "session-newer", name: "列表 新名称"}) {
		t.Fatalf("rename calls=%#v", calls)
	}
	if !strings.Contains(reply, "已重命名 Claude 会话") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestClaudeRenameRejectsInvalidNames(t *testing.T) {
	commands := []string{
		"/cc rename current",
		"/cc rename current \n下一行",
		"/cc rename current 包含\x01控制符",
		"/cc rename current " + strings.Repeat("名", 121),
	}
	for index, command := range commands {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			h, ag, _, _ := newClaudeRenameCommandFixture(t, "session-current")
			reply := h.handleClaudeSessionCommand(context.Background(), "user-1", command)
			if len(ag.calls()) != 0 {
				t.Fatalf("invalid name reached agent: %#v", ag.calls())
			}
			if !strings.Contains(reply, "名称") && !strings.Contains(reply, "用法") {
				t.Fatalf("invalid-name reply=%q", reply)
			}
		})
	}
}

func TestClaudeRenameRejectsActiveWriter(t *testing.T) {
	h, ag, _, _ := newClaudeRenameCommandFixture(t, "session-current")
	taskKey := claudeSessionExecutionKey("session-current")
	task, _, started := h.beginActiveTask(context.Background(), taskKey, activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "claude", sessionID: "session-current",
	})
	if !started {
		t.Fatal("failed to seed active Claude task")
	}
	defer h.finishActiveTask(taskKey, task)

	reply := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc rename current 新名称")

	if len(ag.calls()) != 0 || !strings.Contains(reply, "仍有任务") {
		t.Fatalf("rename calls=%#v reply=%q", ag.calls(), reply)
	}
}

func TestClaudeRenameUnknownOutcomePreservesBinding(t *testing.T) {
	h, ag, workspace, bindingKey := newClaudeRenameCommandFixture(t, "session-current")
	ag.renameErr = agent.ErrClaudeRenameOutcomeUnknown

	reply := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc rename current 新名称")

	binding := h.ensureClaudeSessions().binding(bindingKey)
	if binding.SessionID != "session-current" || binding.WorkspaceRoot != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("binding changed: %+v", binding)
	}
	if !strings.Contains(reply, "结果暂") || !strings.Contains(reply, "/cc ls") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestClaudeRenameUnsupportedDoesNotChangeBinding(t *testing.T) {
	h, ag, _, bindingKey := newClaudeRenameCommandFixture(t, "session-current")
	ag.renameErr = agent.ErrClaudeRenameUnsupported

	reply := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc rename current 新名称")

	if binding := h.ensureClaudeSessions().binding(bindingKey); binding.SessionID != "session-current" {
		t.Fatalf("binding changed: %+v", binding)
	}
	if !strings.Contains(reply, "未公布") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestClaudeRenameConfirmedFailurePreservesBinding(t *testing.T) {
	h, ag, _, bindingKey := newClaudeRenameCommandFixture(t, "session-current")
	ag.renameErr = errors.New("preflight failed")

	reply := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc rename current 新名称")

	if binding := h.ensureClaudeSessions().binding(bindingKey); binding.SessionID != "session-current" {
		t.Fatalf("binding changed: %+v", binding)
	}
	if !strings.Contains(reply, "重命名失败") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func newClaudeRenameCommandFixture(t *testing.T, sessionID string) (*Handler, *fakeClaudeRenameAgent, string, string) {
	t.Helper()
	h, base, workspace := newClaudeACPNavigationHandler(t)
	base.catalogSessions = []agent.ClaudeSession{{ID: sessionID, Cwd: workspace, Title: "旧名称"}}
	ag := &fakeClaudeRenameAgent{fakeClaudeSessionAgent: base}
	h.agents["claude"] = ag
	bindingKey := claudeBindingKey("user-1", "claude")
	if err := h.ensureClaudeSessions().commitSelection(bindingKey, workspace, sessionID); err != nil {
		t.Fatal(err)
	}
	return h, ag, workspace, bindingKey
}
