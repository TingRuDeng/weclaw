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

type fakeCodexArchiveAgent struct {
	*fakeCodexLiveAgent
	mu                    sync.Mutex
	archiveCalls          []string
	archiveErr            error
	threadArchivedHandler func(string)
}

func newFakeCodexArchiveAgent(threadID string) *fakeCodexArchiveAgent {
	base := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: threadID})
	base.fakeCodexThreadAgent.threadID = threadID
	return &fakeCodexArchiveAgent{fakeCodexLiveAgent: base}
}

func (f *fakeCodexArchiveAgent) ArchiveCodexThread(_ context.Context, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archiveCalls = append(f.archiveCalls, threadID)
	if f.archiveErr == nil || errors.Is(f.archiveErr, agent.ErrCodexArchiveOutcomeUnknown) {
		f.fakeCodexThreadAgent.threadID = ""
	}
	return f.archiveErr
}

func (f *fakeCodexArchiveAgent) archivedThreads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.archiveCalls...)
}

func (f *fakeCodexArchiveAgent) SetCodexThreadArchivedHandler(handler func(string)) {
	f.mu.Lock()
	f.threadArchivedHandler = handler
	f.mu.Unlock()
}

func (f *fakeCodexArchiveAgent) emitThreadArchived(threadID string) {
	f.mu.Lock()
	handler := f.threadArchivedHandler
	f.mu.Unlock()
	if handler != nil {
		handler(threadID)
	}
}

func TestCodexArchiveCurrentClearsFrontendBinding(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexArchiveCommandFixture(t, "thread-current")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive current", Platform: platform.PlatformWeChat,
	})

	if calls := ag.archivedThreads(); len(calls) != 1 || calls[0] != "thread-current" {
		t.Fatalf("archive calls=%q", calls)
	}
	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "" || pending {
		t.Fatalf("binding after archive thread=%q pending=%v", threadID, pending)
	}
	if !strings.Contains(reply, "已归档 Codex 会话") || strings.Contains(reply, "thread-current") {
		t.Fatalf("archive reply=%q", reply)
	}
}

func TestCodexArchiveListIndexUsesBrowsedWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-newer", workspace, "较新会话", "2026-04-29T10:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-older", workspace, "较早会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexArchiveAgent("")
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.setCodexBrowseWorkspace(bindingKey, workspace)

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive 1", Platform: platform.PlatformFeishu,
	})

	if calls := ag.archivedThreads(); len(calls) != 1 || calls[0] != "thread-newer" {
		t.Fatalf("archive calls=%q", calls)
	}
	if !strings.Contains(reply, "较新会话") || !strings.Contains(reply, "已归档 Codex 会话") {
		t.Fatalf("archive reply=%q", reply)
	}
}

func TestCodexArchiveRejectsRunningTask(t *testing.T) {
	h, ag, _, _ := newCodexArchiveCommandFixture(t, "thread-busy")
	admission := h.beginOrQueueActiveTask(context.Background(), "archive-task", activeTaskMeta{
		owner: "user-1", routeUserID: "user-1", agentName: "codex",
		codexThreadID: "thread-busy",
	}, pendingAgentTask{})
	if admission.status != activeTaskStarted {
		t.Fatalf("task admission=%v", admission.status)
	}
	defer admission.task.cancel()

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive current", Platform: platform.PlatformWeChat,
	})

	if calls := ag.archivedThreads(); len(calls) != 0 {
		t.Fatalf("archive calls=%q", calls)
	}
	if !strings.Contains(reply, "仍有任务") {
		t.Fatalf("archive reply=%q", reply)
	}
}

func TestCodexArchiveRejectsThreadBoundByAnotherFrontend(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexArchiveCommandFixture(t, "thread-shared")
	otherKey := codexBindingKey("user-2", "codex")
	h.ensureCodexSessions().setThread(otherKey, workspace, "thread-shared")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive current", Platform: platform.PlatformWeChat,
	})

	if calls := ag.archivedThreads(); len(calls) != 0 {
		t.Fatalf("archive calls=%q", calls)
	}
	if !strings.Contains(reply, "其他窗口") {
		t.Fatalf("archive reply=%q", reply)
	}
	if threadID, _ := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-shared" {
		t.Fatalf("caller binding=%q", threadID)
	}
}

func TestCodexArchiveConfirmedFailureRollsBackBinding(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexArchiveCommandFixture(t, "thread-current")
	ag.archiveErr = errors.New("archive preflight failed")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive current", Platform: platform.PlatformWeChat,
	})

	if threadID, _ := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-current" {
		t.Fatalf("binding after confirmed failure=%q", threadID)
	}
	if !strings.Contains(reply, "归档失败") {
		t.Fatalf("archive reply=%q", reply)
	}
}

func TestCodexArchiveUnknownOutcomeKeepsFrontendUnbound(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexArchiveCommandFixture(t, "thread-current")
	ag.archiveErr = agent.ErrCodexArchiveOutcomeUnknown

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive current", Platform: platform.PlatformWeChat,
	})

	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "" || pending {
		t.Fatalf("binding after unknown outcome thread=%q pending=%v", threadID, pending)
	}
	if !strings.Contains(reply, "结果暂时无法确认") || !strings.Contains(reply, "/cx ls") {
		t.Fatalf("archive reply=%q", reply)
	}
}

func TestCodexArchiveListUnknownOutcomePreservesCurrentBinding(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-target", workspace, "待归档会话", "2026-04-29T10:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexArchiveAgent("")
	ag.archiveErr = agent.ErrCodexArchiveOutcomeUnknown
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-current")
	h.setCodexBrowseWorkspace(bindingKey, workspace)
	targetIndex := -1
	for index := 0; index < 10; index++ {
		view, found, err := h.resolveCodexSessionByIndex(bindingKey, index)
		if err != nil {
			t.Fatalf("resolve archive target %d: %v", index, err)
		}
		if !found {
			break
		}
		if view.ThreadID == "thread-target" {
			targetIndex = index
			break
		}
	}
	if targetIndex < 0 {
		t.Fatal("thread-target not found in browsed workspace")
	}

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: fmt.Sprintf("/cx archive %d", targetIndex+1), Platform: platform.PlatformWeChat,
	})

	if calls := ag.archivedThreads(); len(calls) != 1 || calls[0] != "thread-target" {
		t.Fatalf("archive calls=%q", calls)
	}
	if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-current" || pending {
		t.Fatalf("binding after indexed unknown outcome thread=%q pending=%v", threadID, pending)
	}
	if !strings.Contains(reply, "当前窗口原有绑定未改变") || strings.Contains(reply, "当前窗口已解除") {
		t.Fatalf("archive reply=%q", reply)
	}
}

func TestCodexArchiveRequiresArchiveCapableAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-current"})
	ag.fakeCodexThreadAgent.threadID = "thread-current"
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-current")

	reply := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1",
		Trimmed: "/cx archive current", Platform: platform.PlatformWeChat,
	})

	if !strings.Contains(reply, "不支持归档") {
		t.Fatalf("archive reply=%q", reply)
	}
	if threadID, _ := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "thread-current" {
		t.Fatalf("unsupported archive changed binding=%q", threadID)
	}
}

func TestCodexExternalArchiveNotificationClearsAndPersistsEveryFrontendBinding(t *testing.T) {
	h := NewHandler(nil, nil)
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	h.ensureCodexSessions().SetFilePath(stateFile)
	workspace := filepath.Join(t.TempDir(), "project")
	firstKey := codexBindingKey("user-1", "codex")
	secondKey := codexBindingKey("user-2", "codex")
	h.ensureCodexSessions().setThread(firstKey, workspace, "thread-external")
	h.ensureCodexSessions().setThread(secondKey, workspace, "thread-external")
	ag := newFakeCodexArchiveAgent("thread-external")
	h.SetDefaultAgent("codex", ag)

	ag.emitThreadArchived("thread-external")

	for _, bindingKey := range []string{firstKey, secondKey} {
		if threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace); threadID != "" || pending {
			t.Fatalf("binding %q after external archive thread=%q pending=%v", bindingKey, threadID, pending)
		}
	}
	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(stateFile)
	update := codexRemoteSelectionUpdate{
		BindingKey: firstKey, WorkspaceRoot: workspace,
		TargetThreadID: "thread-external", ConversationID: "conversation-a",
		Expected: reloaded.remoteSelectionSnapshot(firstKey, "thread-external"),
	}
	if _, err := reloaded.commitRemoteSelection(update); !errors.Is(err, errCodexRemoteThreadArchived) {
		t.Fatalf("selection after reload error=%v, want archived tombstone", err)
	}
}

func newCodexArchiveCommandFixture(
	t *testing.T,
	threadID string,
) (*Handler, *fakeCodexArchiveAgent, string, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexArchiveAgent(threadID)
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
	h.ensureCodexSessions().setThread(bindingKey, workspace, threadID)
	return h, ag, workspace, bindingKey
}
