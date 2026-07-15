package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexCxCdWorkspaceThenLsListsSessionsWithoutThreadIDs(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspace := filepath.Join(root, "weclaw")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-local-a", workspace, "实现两级会话浏览", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-local-b", workspace, "修复安全问题", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(112, "/cx cd 0"))

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	text := strings.Join(calls.texts(), "\n")
	if strings.Contains(text, "已进入工作空间") {
		t.Fatalf("cd reply should not include redundant title, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "工作空间: weclaw") || !strings.Contains(text, "weclaw 会话") {
		t.Fatalf("cd reply should enter workspace and show sessions, messages=%#v", calls.texts())
	}
	if !strings.Contains(text, "0. 实现两级会话浏览") || !strings.Contains(text, "1. 修复安全问题") {
		t.Fatalf("session ls should show numbered session names, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread-local-a") || strings.Contains(text, "来源:") {
		t.Fatalf("session ls should hide thread ids and source labels, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceUsesCodexAppThreadList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-jsonl-a", workspace, "JSONL 旧会话 A", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-jsonl-b", workspace, "JSONL 旧会话 B", "2026-04-29T08:00:00Z")
	writeLocalCodexIndex(t, codexDir, "thread-app-new", "App 重命名会话", "2026-04-29T10:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[{"id":"thread-app-new","title":"App 新会话\n第二行不展示","recency_at_ms":2000},{"id":"thread-app-old","title":"App 旧会话","recency_at_ms":1000}]`)
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

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(151, "/cx cd 0"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. App 重命名会话") || !strings.Contains(text, "1. App 旧会话") {
		t.Fatalf("session ls should use Codex App thread order, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "JSONL 旧会话") || strings.Contains(text, "App 新会话") || strings.Contains(text, "第二行不展示") {
		t.Fatalf("session ls should hide JSONL fallback, raw app title and multiline title tail, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceHidesCodexAppSubagentThreads(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-jsonl", workspace, "JSONL 旧会话", "2026-04-29T09:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[
{"id":"thread-app-user","title":"App 主会话","recency_at_ms":3000,"source":"vscode","thread_source":"user"},
{"id":"thread-app-user-2","title":"App 第二会话","recency_at_ms":2500,"source":"vscode","thread_source":"user"},
{"id":"thread-app-guardian","title":"The following is the Codex agent history whose request action you are assessing.","recency_at_ms":2000,"source":"{\"subagent\":{\"other\":\"guardian\"}}","thread_source":"subagent"},
{"id":"thread-app-spawn","title":"内部子任务","recency_at_ms":1000,"source":"{\"subagent\":{\"thread_spawn\":{\"parent_thread_id\":\"thread-app-user\"}}}","thread_source":"user"}
]`)
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

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(154, "/cx cd 0"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "0. App 主会话") || !strings.Contains(text, "1. App 第二会话") {
		t.Fatalf("session ls should show app user thread, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "Codex agent history") || strings.Contains(text, "内部子任务") || strings.Contains(text, "JSONL 旧会话") {
		t.Fatalf("session ls should hide subagent and JSONL fallback threads, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceSkipsStoredArchivedThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-archived", workspace, "已归档旧缓存", "2026-04-29T09:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[{"id":"thread-visible","title":"App 可见会话","recency_at_ms":2000}]`)
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-archived")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(152, "/cx cd 0"))

	intent := h.codexSessions.controlIntent("thread-visible")
	if ag.useThreadID != "" || intent.Owner != codexControlRemote {
		t.Fatalf("use=%q intent=%#v", ag.useThreadID, intent)
	}
	if containsText(calls.texts(), "thread-archived") || containsText(calls.texts(), "已归档旧缓存") {
		t.Fatalf("cd should ignore stored archived thread, messages=%#v", calls.texts())
	}
}

func TestCodexCxCdWorkspaceClearsStaleStoredThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-archived", workspace, "已归档旧缓存", "2026-04-29T09:00:00Z")
	writeCodexAppWorkspaceState(t, codexDir, []string{workspace}, []string{workspace})
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[{"id":"thread-visible-a","title":"App 可见会话 A","recency_at_ms":3000},{"id":"thread-visible-b","title":"App 可见会话 B","recency_at_ms":2000}]`)
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-archived")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(153, "/cx cd 0"))

	threadID, pending := h.codexSessions.getThread(bindingKey, workspace)
	if threadID != "" || pending {
		t.Fatalf("stale stored thread=%q pending=%v, want empty false", threadID, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "App 可见会话 A") || !strings.Contains(text, "App 可见会话 B") {
		t.Fatalf("cd should still show app visible sessions, messages=%#v", calls.texts())
	}
}
