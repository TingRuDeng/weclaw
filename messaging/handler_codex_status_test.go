package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexStatusKeepsFreshRemoteThreadBeforeFirstTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake sqlite db: %v", err)
	}
	writeFakeSQLite3(t, `[]`)
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeUnknown, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(136, "/cx new"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(137, "/cx status"))

	bindingKey := codexBindingKey("user-1", "codex")
	threadID, pending := h.codexSessions.getThread(bindingKey, workspace)
	if threadID != "thread-new" || pending {
		t.Fatalf("status cleared fresh remote thread=%q pending=%v, messages=%#v", threadID, pending, calls.texts())
	}
	if _, err := h.resolveCodexConversationIDForRoute(
		context.Background(), "user-1", "user-1", "codex", ag,
	); err != nil {
		t.Fatalf("first message should keep using fresh remote thread: %v", err)
	}
}

func TestCodexStatusShowsWorkspaceThreadAndLocalEntryState(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(131, "/cx status"))

	text := strings.Join(calls.texts(), "\n")
	for _, want := range []string{
		"Codex 状态",
		"工作空间: " + workspace,
		"会话: 未命名会话",
		"remote: 已配置",
		"CLI: 未打开过",
		"App: 未打开过",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status should contain %q, messages=%#v", want, calls.texts())
		}
	}
}

func TestCodexStatusShowsSessionName(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := t.TempDir()
	writeLocalCodexSession(t, codexDir, "thread-1", workspace, "修复审批体验", "2026-07-01T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(135, "/cx status"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "会话: 修复审批体验") {
		t.Fatalf("status should show session name, messages=%#v", calls.texts())
	}
	if strings.Contains(text, "thread: thread-1") {
		t.Fatalf("status should not show raw thread id, messages=%#v", calls.texts())
	}
}

func TestCodexStatusRecordsSuccessfulLocalEntries(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetCodexCLIResumeOpener(func(_ context.Context, _ string, _ string, _ string) error {
		return nil
	})
	h.SetCodexAppOpener(func(_ context.Context, _ string, _ string) error {
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(132, "/cx cli"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(133, "/cx app"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(134, "/cx status"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "CLI: 已打开过") || !strings.Contains(text, "App: 已打开过") {
		t.Fatalf("status should record successful local entries, messages=%#v", calls.texts())
	}
}
