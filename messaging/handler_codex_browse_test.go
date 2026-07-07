package messaging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestDiscoverLocalCodexSessionsReadsIndexAndSessionMeta(t *testing.T) {
	codexDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "桌面会话 A", "2026-04-28T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "桌面会话 B", "2026-04-29T08:00:00Z")

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 2 {
		t.Fatalf("sessions len=%d, want 2: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-b" || sessions[0].WorkspaceRoot != normalizeCodexWorkspaceRoot(workspaceB) {
		t.Fatalf("first session=%#v, want newest thread-b workspace-b", sessions[0])
	}
	if sessions[1].ThreadName != "桌面会话 A" {
		t.Fatalf("second thread name=%q, want 桌面会话 A", sessions[1].ThreadName)
	}
}

func TestDiscoverLocalCodexSessionsSkipsArchivedSessions(t *testing.T) {
	codexDir := t.TempDir()
	activeWorkspace := filepath.Join(t.TempDir(), "active")
	archivedWorkspace := filepath.Join(t.TempDir(), "archived")
	writeLocalCodexSession(t, codexDir, "thread-active", activeWorkspace, "活跃会话", "2026-04-29T09:00:00Z")
	writeArchivedLocalCodexSession(t, codexDir, "thread-archived", archivedWorkspace, "归档会话", "2026-04-29T08:00:00Z")

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-active" {
		t.Fatalf("session thread=%q, want thread-active", sessions[0].ThreadID)
	}
}

func TestDiscoverLocalCodexSessionsSkipsHiddenDesktopSessions(t *testing.T) {
	codexDir := t.TempDir()
	visibleWorkspace := filepath.Join(t.TempDir(), "visible")
	writeLocalCodexSession(t, codexDir, "thread-visible", visibleWorkspace, "桌面主会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSessionMeta(t, codexDir, "thread-subagent", filepath.Join(t.TempDir(), "subagent"), "2026-04-29T08:00:00Z", `"Codex Desktop"`, `"subagent"`, `{"subagent":{"thread_spawn":{"parent_thread_id":"thread-visible"}}}`)
	writeLocalCodexSessionMeta(t, codexDir, "thread-cli", filepath.Join(t.TempDir(), "cli"), "2026-04-29T07:00:00Z", `"codex-tui"`, `"user"`, `"vscode"`)
	writeLocalCodexIndex(t, codexDir, "thread-subagent", "子代理会话", "2026-04-29T08:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-cli", "CLI 会话", "2026-04-29T07:00:00Z")

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-visible" {
		t.Fatalf("session thread=%q, want thread-visible", sessions[0].ThreadID)
	}
}

func TestDiscoverLocalCodexSessionsSkipsMissingWorkspace(t *testing.T) {
	codexDir := t.TempDir()
	existingWorkspace := filepath.Join(t.TempDir(), "existing")
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	if err := os.MkdirAll(existingWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir existing workspace: %v", err)
	}
	writeLocalCodexSession(t, codexDir, "thread-existing", existingWorkspace, "现存工作空间", "2026-04-29T09:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-missing", "已删除工作空间", "2026-04-29T10:00:00Z")
	writeLocalCodexSessionMeta(t, codexDir, "thread-missing", missingWorkspace, "2026-04-29T10:00:00Z", `"Codex Desktop"`, `""`, `"vscode"`)

	sessions := discoverLocalCodexSessions(codexDir)

	if len(sessions) != 1 {
		t.Fatalf("sessions len=%d, want 1: %#v", len(sessions), sessions)
	}
	if sessions[0].ThreadID != "thread-existing" {
		t.Fatalf("session thread=%q, want thread-existing", sessions[0].ThreadID)
	}
}

func TestCodexLsIncludesLocalCodexSessionsAndDeduplicatesRecordedThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	recordedWorkspace := filepath.Join(t.TempDir(), "recorded")
	localWorkspace := filepath.Join(t.TempDir(), "local")
	writeLocalCodexSession(t, codexDir, "thread-recorded", recordedWorkspace, "重复会话", "2026-04-29T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-local", localWorkspace, "桌面本机会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": recordedWorkspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), recordedWorkspace, "thread-recorded")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(109, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. local") || !strings.Contains(text, "1. recorded") {
		t.Fatalf("ls should include local and recorded workspace names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-recorded") || strings.Contains(text, "来源:") {
		t.Fatalf("workspace ls should hide thread ids and source labels, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandBindsLocalCodexSessionIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalCodexSession(t, codexDir, "thread-desktop", workspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(110, "/cx switch 0"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-desktop" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-desktop)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-desktop" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-desktop false", thread, pending)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchFailureDoesNotLeakThreadStoreErrorOrSwitchWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	currentWorkspace := filepath.Join(t.TempDir(), "current")
	localWorkspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalCodexSession(t, codexDir, "thread-bad", localWorkspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:    agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			lastCwd: currentWorkspace,
		},
		useErr: fmt.Errorf("resume thread thread-bad: agent error: failed to read thread: thread-store internal error: failed to read thread /tmp/rollout.jsonl: rollout does not start with session metadata"),
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(116, "/cx switch 0"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "该 Codex 会话当前无法被微信接手") || !strings.Contains(text, "/cx app") {
		t.Fatalf("reply should explain local thread resume failure, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-store internal error") || strings.Contains(text, "session metadata") {
		t.Fatalf("reply should hide internal thread-store details, messages=%#v", calls.texts())
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(currentWorkspace) {
		t.Fatalf("codex cwd=%q, want unchanged %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(currentWorkspace))
	}
}

func TestCodexCxLsListsWorkspacesWithoutThreads(t *testing.T) {
	h := NewHandler(nil, nil)
	root := t.TempDir()
	workspaceA := filepath.Join(root, "weclaw")
	workspaceB := filepath.Join(root, "card-manager-android")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(111, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "Codex 工作空间") {
		t.Fatalf("ls should show workspace list, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "0. card-manager-android") || !strings.Contains(text, "1. weclaw") {
		t.Fatalf("ls should show workspace short names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-a") || strings.Contains(text, workspaceA) {
		t.Fatalf("workspace ls should hide thread ids and full paths, messages=%#v", calls.texts())
	}
}

func TestCodexCxLsUsesCodexAppWorkspaceOrder(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	weclawWorkspace := filepath.Join(root, "weclaw")
	safariWorkspace := filepath.Join(root, "SafariCollection")
	tmpWorkspace := filepath.Join(root, "tmp")
	writeLocalCodexSession(t, codexDir, "thread-weclaw", weclawWorkspace, "WeClaw 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-safari", safariWorkspace, "Safari 会话", "2026-04-29T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-tmp", tmpWorkspace, "历史临时会话", "2026-04-29T10:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{weclawWorkspace, safariWorkspace}, []string{weclawWorkspace, safariWorkspace})
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(150, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. weclaw") || !strings.Contains(text, "1. SafariCollection") {
		t.Fatalf("ls should follow Codex App project order, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "tmp") {
		t.Fatalf("ls should hide workspaces not in Codex App project order, messages=%#v", calls.texts())
	}
}
