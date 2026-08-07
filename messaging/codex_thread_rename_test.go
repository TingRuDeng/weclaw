package messaging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

type fakeCodexRenameAgent struct {
	*fakeCodexLiveAgent
	mu          sync.Mutex
	renameCalls []codexRenameCall
	renameErr   error
}

type codexRenameCall struct {
	threadID string
	name     string
}

func newFakeCodexRenameAgent(threadID string) *fakeCodexRenameAgent {
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: threadID})
	base.fakeCodexThreadAgent.threadID = threadID
	return &fakeCodexRenameAgent{fakeCodexLiveAgent: base}
}

func (f *fakeCodexRenameAgent) RenameCodexThread(_ context.Context, threadID string, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameCalls = append(f.renameCalls, codexRenameCall{threadID: threadID, name: name})
	return f.renameErr
}

func (f *fakeCodexRenameAgent) calls() []codexRenameCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codexRenameCall(nil), f.renameCalls...)
}

func TestCodexRenameCurrentPreservesNameSpacesAndBindings(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexRenameCommandFixture(t, "thread-current")
	otherKey := codexBindingKey("user-2", "codex")
	h.ensureCodexSessions().setThread(otherKey, workspace, "thread-current")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx rename current 新 的 名称", Platform: platform.PlatformWeChat,
	})

	if calls := ag.calls(); len(calls) != 1 || calls[0] != (codexRenameCall{threadID: "thread-current", name: "新 的 名称"}) {
		t.Fatalf("rename calls=%#v", calls)
	}
	for _, key := range []string{bindingKey, otherKey} {
		if threadID, pending := h.ensureCodexSessions().getThread(key, workspace); threadID != "thread-current" || pending {
			t.Fatalf("binding %q changed to thread=%q pending=%v", key, threadID, pending)
		}
	}
	if !strings.Contains(reply, "已重命名 Codex 会话") || strings.Contains(reply, "新 的 名称") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestCodexRenameListIndexUsesBrowsedWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-newer", workspace, "较新会话", "2026-04-29T10:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexRenameAgent("")
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.setCodexBrowseWorkspace(bindingKey, workspace)

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx rename 1 列表 新名称", Platform: platform.PlatformFeishu,
	})

	if calls := ag.calls(); len(calls) != 1 || calls[0] != (codexRenameCall{threadID: "thread-newer", name: "列表 新名称"}) {
		t.Fatalf("rename calls=%#v", calls)
	}
	if !strings.Contains(reply, "已重命名 Codex 会话") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestCodexRenameRejectsInvalidNames(t *testing.T) {
	tests := []string{
		"/cx rename current",
		"/cx rename current \n下一行",
		"/cx rename current 包含\x01控制符",
		"/cx rename current " + strings.Repeat("名", 121),
	}
	for index, command := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			h, ag, _, _ := newCodexRenameCommandFixture(t, "thread-current")
			reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
				ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: command, Platform: platform.PlatformWeChat,
			})
			if len(ag.calls()) != 0 {
				t.Fatalf("invalid name reached agent: %#v", ag.calls())
			}
			if !strings.Contains(reply, "名称") && !strings.Contains(reply, "用法") {
				t.Fatalf("invalid-name reply=%q", reply)
			}
		})
	}
}

func TestCodexRenameRejectsRunningTask(t *testing.T) {
	h, ag, _, _ := newCodexRenameCommandFixture(t, "thread-busy")
	admission := h.beginOrQueueActiveTask(context.Background(), "rename-task", activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "codex", codexThreadID: "thread-busy",
	}, pendingAgentTask{})
	if admission.status != activeTaskStarted {
		t.Fatalf("task admission=%v", admission.status)
	}
	defer admission.task.cancel()

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx rename current 新名称", Platform: platform.PlatformWeChat,
	})

	if len(ag.calls()) != 0 || !strings.Contains(reply, "仍有任务") {
		t.Fatalf("rename calls=%#v reply=%q", ag.calls(), reply)
	}
}

func TestCodexRenameUnknownOutcomePreservesBinding(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexRenameCommandFixture(t, "thread-current")
	ag.renameErr = agent.ErrCodexRenameOutcomeUnknown

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx rename current 新名称", Platform: platform.PlatformWeChat,
	})

	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-current" || pending {
		t.Fatalf("binding changed to thread=%q pending=%v", threadID, pending)
	}
	if !strings.Contains(reply, "结果暂") || !strings.Contains(reply, "/cx ls") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func TestCodexRenameConfirmedFailurePreservesBinding(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexRenameCommandFixture(t, "thread-current")
	ag.renameErr = errors.New("preflight failed")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx rename current 新名称", Platform: platform.PlatformWeChat,
	})

	if threadID, _ := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-current" {
		t.Fatalf("binding changed to %q", threadID)
	}
	if !strings.Contains(reply, "重命名失败") {
		t.Fatalf("rename reply=%q", reply)
	}
}

func newCodexRenameCommandFixture(t *testing.T, threadID string) (*Handler, *fakeCodexRenameAgent, string, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexRenameAgent(threadID)
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, threadID)
	return h, ag, workspace, bindingKey
}
