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

func TestReadLocalCodexSessionIndexContinuesAfterLargeRecord(t *testing.T) {
	codexDir := t.TempDir()
	writeLocalCodexIndex(t, codexDir, "thread-large", strings.Repeat("x", 70*1024), "2026-04-29T07:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-target", "App 当前会话名", "2026-04-29T08:00:00Z")

	index := readLocalCodexSessionIndex(filepath.Join(codexDir, "session_index.jsonl"))

	if got := index["thread-target"].ThreadName; got != "App 当前会话名" {
		t.Fatalf("target thread name=%q, want App 当前会话名", got)
	}
}

func TestReadLocalCodexSessionIndexSkipsBoundedOversizedRecord(t *testing.T) {
	codexDir := t.TempDir()
	writeLocalCodexIndex(t, codexDir, "thread-oversized", strings.Repeat("x", codexLocalIndexMaxRecordBytes+1), "2026-04-29T07:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-target", "超限记录后的会话", "2026-04-29T08:00:00Z")

	index := readLocalCodexSessionIndex(filepath.Join(codexDir, "session_index.jsonl"))

	if _, exists := index["thread-oversized"]; exists {
		t.Fatal("oversized index record must be skipped")
	}
	if got := index["thread-target"].ThreadName; got != "超限记录后的会话" {
		t.Fatalf("target thread name=%q, want 超限记录后的会话", got)
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
	h.SetAllowedWorkspaceRoots([]string{recordedWorkspace, localWorkspace})
	writeLocalCodexSession(t, codexDir, "thread-recorded", recordedWorkspace, "重复会话", "2026-04-29T08:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-local", localWorkspace, "桌面本机会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": recordedWorkspace})
	h.ensureCodexSessions().setThread(codexBindingKey("user-1", "codex"), recordedWorkspace, "thread-recorded")

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
	appendLocalCodexTurnContext(t, codexDir, "thread-desktop", "gpt-old", "low")
	appendLocalCodexTurnContext(t, codexDir, "thread-desktop", "gpt-5.5", "high")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(110, "/cx switch 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	thread, pending := h.ensureCodexSessions().getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-desktop" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-desktop false", thread, pending)
	}
	if !containsText(calls.texts(), "已切换并绑定") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "模型: gpt-5.5") || !strings.Contains(text, "推理强度: high") {
		t.Fatalf("reply should show latest session model status, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchRuntimeFailureKeepsCommittedSelection(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	currentWorkspace := filepath.Join(t.TempDir(), "current")
	localWorkspace := filepath.Join(t.TempDir(), "desktop")
	writeLocalCodexSession(t, codexDir, "thread-bad", localWorkspace, "桌面会话", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.handoffErrors["thread-bad"] = fmt.Errorf("探测失败")
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, currentWorkspace, "thread-current")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, currentWorkspace)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(116, "/cx switch thread-bad"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已切换并绑定") || !strings.Contains(text, "运行通道: 暂不可用") {
		t.Fatalf("reply should preserve binding while reporting runtime failure, messages=%#v", calls.texts())
	}
	active, _ := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	currentThread, _ := h.ensureCodexSessions().getThread(bindingKey, currentWorkspace)
	targetThread, pending := h.ensureCodexSessions().getThread(bindingKey, localWorkspace)
	if active != localWorkspace || currentThread != "thread-current" || targetThread != "thread-bad" || pending {
		t.Fatalf("active=%q current=%q target=%q pending=%t", active, currentThread, targetThread, pending)
	}
}

func TestCodexCxLsListsWorkspacesWithoutThreads(t *testing.T) {
	h := NewHandler(nil, nil)
	root := t.TempDir()
	workspaceA := filepath.Join(root, "weclaw")
	workspaceB := filepath.Join(root, "card-manager-android")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("创建测试工作空间失败: %v", err)
		}
	}
	h.SetAllowedWorkspaceRoots([]string{root})
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
	h.ensureCodexSessions().setThread(bindingKey, workspaceA, "thread-a")
	h.ensureCodexSessions().setThread(bindingKey, workspaceB, "thread-b")
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
	h.SetAllowedWorkspaceRoots([]string{root})
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

func TestCodexCxLsIncludesProjectStoredOnlyInLocalProjects(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	legacyWorkspace := filepath.Join(root, "weclaw")
	chatGPTWorkspace := filepath.Join(root, ".codex", ".chatgpt-projects", "g-p-smart-home")
	mustCreateWorkspaceDirs(t, legacyWorkspace, chatGPTWorkspace)
	writeCodexAppWorkspaceStateWithProjects(t, codexDir,
		[]string{"local-weclaw"},
		[]string{legacyWorkspace},
		map[string]any{
			"local-weclaw": map[string]any{
				"name":      "weclaw",
				"rootPaths": []string{legacyWorkspace},
				"updatedAt": 1,
			},
			"g-p-smart-home": map[string]any{
				"name":      "智能家居总控",
				"rootPaths": []string{chatGPTWorkspace},
				"updatedAt": 2,
			},
		},
	)
	h.SetCodexLocalSessionDir(codexDir)

	text := h.renderCodexWorkspaceListForAccess(codexBindingKey("admin-1", "codex"), "admin-1", true)
	if !strings.Contains(text, "0. 智能家居总控") || !strings.Contains(text, "1. weclaw") {
		t.Fatalf("ls should include project stored only in local-projects, text=%q", text)
	}
	if strings.Contains(text, "g-p-smart-home") {
		t.Fatalf("ls should use the Codex App project name instead of its internal id, text=%q", text)
	}
}

func TestRenderCodexWorkspaceListDoesNotEagerlyLoadWorkspaceSessions(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "project-a")
	workspaceB := filepath.Join(root, "project-b")
	mustCreateWorkspaceDirs(t, workspaceA, workspaceB)
	writeCodexAppWorkspaceStateWithProjects(t, codexDir,
		[]string{"project-a", "project-b"},
		[]string{workspaceA, workspaceB},
		map[string]any{
			"project-a": map[string]any{"name": "project-a", "rootPaths": []string{workspaceA}, "updatedAt": 2},
			"project-b": map[string]any{"name": "project-b", "rootPaths": []string{workspaceB}, "updatedAt": 1},
		},
	)
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake state database: %v", err)
	}
	callLog := filepath.Join(t.TempDir(), "sqlite-calls")
	writeCountingFakeSQLite3(t, callLog)
	h.SetCodexLocalSessionDir(codexDir)

	text := h.renderCodexWorkspaceListForAccess(codexBindingKey("admin-1", "codex"), "admin-1", true)
	if !strings.Contains(text, "project-a") || !strings.Contains(text, "project-b") {
		t.Fatalf("workspace list=%q", text)
	}
	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read sqlite call log: %v", err)
	}
	if calls := strings.Count(string(data), "\n"); calls != 1 {
		t.Fatalf("sqlite calls=%d, want one project-recency query without per-workspace session loading", calls)
	}

	if err := os.WriteFile(callLog, nil, 0o600); err != nil {
		t.Fatalf("reset sqlite call log: %v", err)
	}
	group, err := h.findCodexWorkspaceGroupForAccess(
		codexBindingKey("admin-1", "codex"), "admin-1", true, "project-b",
	)
	if err != nil || group.Name != "project-b" {
		t.Fatalf("selected group=%#v err=%v", group, err)
	}
	data, err = os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read selected-workspace sqlite call log: %v", err)
	}
	if calls := strings.Count(string(data), "\n"); calls != 2 {
		t.Fatalf("sqlite calls=%d, want project-recency plus only the selected workspace session query", calls)
	}
}

func writeCountingFakeSQLite3(t *testing.T, callLog string) {
	t.Helper()
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf 'call\\n' >> %q\nprintf '[]\\n'\n", callLog)
	path := filepath.Join(binDir, "sqlite3")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write counting fake sqlite3: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
