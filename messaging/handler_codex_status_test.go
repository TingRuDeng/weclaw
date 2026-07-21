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

func TestCodexStatusShowsWorkspaceThreadAndSharedHostState(t *testing.T) {
	h := NewHandler(nil, nil)
	// 隔离开发机上的真实 Codex App 数据；本用例验证的是运行状态渲染。
	h.SetCodexLocalSessionDir(t.TempDir())
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
		"运行模式: 单一共享 app-server",
		"窗口角色: frontend binding",
		"不再启动独立 Codex App、CLI 或 Companion writer",
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
