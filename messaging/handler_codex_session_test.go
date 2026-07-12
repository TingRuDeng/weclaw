package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

func TestSendToNamedCodexUsesWorkspaceConversationAndRecordsThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply: "ok",
			info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-1",
	}
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, _, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	reply := wechat.NewReplier(client, "user-1", "ctx-1", "client-1")
	h.sendToNamedAgent(context.Background(), platform.PlatformWeChat, "user-1", "user-1", reply, "codex", "hello", "client-1")

	waitForFakeAgentCalls(t, &ag.fakeAgent, 1)
	if ag.chatCallCount() != 1 {
		t.Fatalf("codex chat calls=%d, want 1", ag.chatCallCount())
	}
	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.lastChatConversationID() != wantConversationID {
		t.Fatalf("conversationID=%q, want %q", ag.lastChatConversationID(), wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-1" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-1 false", thread, pending)
	}
}

func TestHandleCodexNewCommandImmediatelyCreatesWorkspaceThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:           agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			resetSessionID: "thread-new",
		},
		threadID: "thread-old",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), workspace, "thread-old")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(102, "/cx new"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.resetConversationID() != wantConversationID {
		t.Fatalf("reset conversationID=%q, want %q", ag.resetConversationID(), wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-new" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-new false", thread, pending)
	}
	if !containsText(calls.texts(), "已创建新的codex会话") {
		t.Fatalf("reply should mention new session, messages=%#v", calls.texts())
	}
}

func TestHandleGlobalNewResetsActiveCodexWorkspaceThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info:           agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
			resetSessionID: "thread-new",
		},
		threadID: "thread-old",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setActiveWorkspace(bindingKey, workspace)
	h.codexSessions.setThread(bindingKey, workspace, "thread-old")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(123, "/new"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.resetConversationID() != wantConversationID {
		t.Fatalf("reset conversation=%q, want %q", ag.resetConversationID(), wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "thread-new" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-new false", thread, pending)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已创建新的codex会话") || strings.Contains(text, "/Users/") {
		t.Fatalf("reply should use default agent name, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandSetsWorkspaceThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(103, "/cx switch thread-2"))

	wantConversationID := buildCodexConversationID("user-1", "codex", workspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-2" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-2)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if thread != "thread-2" || pending {
		t.Fatalf("stored thread=%q pending=%v, want thread-2 false", thread, pending)
	}
	if !containsText(calls.texts(), "已切换会话") {
		t.Fatalf("reply should mention switched session, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandSwitchesWorkspaceForKnownThread(t *testing.T) {
	h := NewHandler(nil, nil)
	currentWorkspace := t.TempDir()
	targetWorkspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	h.codexSessions.setThread(codexBindingKey("user-1", "codex"), targetWorkspace, "thread-target")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(106, "/cx switch thread-target"))

	wantConversationID := buildCodexConversationID("user-1", "codex", targetWorkspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-target" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-target)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != targetWorkspace {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), targetWorkspace)
	}
	if got := h.codexWorkspaceRoot("codex"); got != targetWorkspace {
		t.Fatalf("handler workspace=%q, want %q", got, targetWorkspace)
	}
	if !containsText(calls.texts(), "工作空间: "+filepath.Base(targetWorkspace)) {
		t.Fatalf("reply should mention switched workspace, messages=%#v", calls.texts())
	}
}

func TestHandleCodexSwitchCommandAcceptsListIndex(t *testing.T) {
	h := NewHandler(nil, nil)
	root := t.TempDir()
	currentWorkspace := filepath.Join(root, "a")
	targetWorkspace := filepath.Join(root, "b")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": currentWorkspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, currentWorkspace, "thread-a")
	h.codexSessions.setThread(bindingKey, targetWorkspace, "thread-b")

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(108, "/cx switch 1"))

	wantConversationID := buildCodexConversationID("user-1", "codex", targetWorkspace)
	if ag.useConversation != wantConversationID || ag.useThreadID != "thread-b" {
		t.Fatalf("use conversation/thread=(%q,%q), want (%q,thread-b)", ag.useConversation, ag.useThreadID, wantConversationID)
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(targetWorkspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(targetWorkspace))
	}
	if !containsText(calls.texts(), "工作空间: "+filepath.Base(targetWorkspace)) {
		t.Fatalf("reply should mention switched workspace, messages=%#v", calls.texts())
	}
}
