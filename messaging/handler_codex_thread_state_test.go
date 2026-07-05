package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestResolveAgentConversationIDRestoresActiveWorkspaceAfterRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	bindingKey := codexBindingKey("user-1", "codex")
	defaultWorkspace := t.TempDir()
	activeWorkspace := t.TempDir()

	first := NewHandler(nil, nil)
	first.SetCodexSessionFile(stateFile)
	first.codexSessions.setThread(bindingKey, activeWorkspace, "thread-active")
	first.codexSessions.setActiveWorkspace(bindingKey, activeWorkspace)

	second := NewHandler(nil, nil)
	second.SetCodexSessionFile(stateFile)
	second.SetAgentWorkDirs(map[string]string{"codex": defaultWorkspace})
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}

	conversationID, err := second.resolveAgentConversationID(context.Background(), "user-1", "codex", ag)
	if err != nil {
		t.Fatalf("resolveAgentConversationID error: %v", err)
	}

	wantConversationID := buildCodexConversationID("user-1", "codex", activeWorkspace)
	if conversationID != wantConversationID {
		t.Fatalf("conversationID=%q, want %q", conversationID, wantConversationID)
	}
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-active" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-active)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != activeWorkspace {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), activeWorkspace)
	}
}

func TestSendToNamedCodexDoesNotCreateNewThreadWhenResumeFails(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply: "不应调用",
			info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		useErr: errors.New("resume failed"),
	}
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-old")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendToNamedAgent(context.Background(), platform.PlatformWeChat, "user-1", "user-1", reply, "codex", "继续", "client-1")

	waitForText(t, calls, "恢复 Codex 会话失败")
	if ag.chatCallCount() != 0 {
		t.Fatalf("恢复旧 thread 失败后不应继续新建会话聊天，chatCalls=%d", ag.chatCallCount())
	}
	if ag.useThreadID != "thread-old" {
		t.Fatalf("恢复 thread=%q，want thread-old", ag.useThreadID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-old" || pending {
		t.Fatalf("不应覆盖旧 thread，thread=%q pending=%v", thread, pending)
	}
}

func TestRecordCodexThreadKeepsExistingThreadWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	currentWorkspace := t.TempDir()
	ownerWorkspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-owner",
	}
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, ownerWorkspace, "thread-owner")

	h.recordCodexThread("user-1", "codex", ag, buildCodexConversationID("user-1", "codex", currentWorkspace))

	currentThread, currentPending := h.codexSessions.getThread(bindingKey, currentWorkspace)
	if currentThread != "" || currentPending {
		t.Fatalf("不应把已有 thread 移动到当前 workspace，thread=%q pending=%v", currentThread, currentPending)
	}
	ownerThread, ownerPending := h.codexSessions.getThread(bindingKey, ownerWorkspace)
	if ownerThread != "thread-owner" || ownerPending {
		t.Fatalf("原 workspace thread=%q pending=%v，want thread-owner false", ownerThread, ownerPending)
	}
	active, ok := h.codexSessions.getActiveWorkspace(bindingKey)
	if !ok || active != normalizeCodexWorkspaceRoot(ownerWorkspace) {
		t.Fatalf("active workspace=(%q,%v)，want %q true", active, ok, normalizeCodexWorkspaceRoot(ownerWorkspace))
	}
}

func TestHandleCodexWhoamiAndLsCommands(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-1")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(104, "/codex whoami"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(105, "/codex ls"))

	texts := calls.texts()
	if !containsText(texts, "workspace: "+workspace) {
		t.Fatalf("whoami should include workspace, messages=%#v", texts)
	}
	if !containsText(texts, "thread: thread-1") {
		t.Fatalf("whoami/ls should include thread, messages=%#v", texts)
	}
	if !containsText(texts, "0. "+filepath.Base(workspace)) {
		t.Fatalf("ls should include numbered workspace, messages=%#v", texts)
	}
}
