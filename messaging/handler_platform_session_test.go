package messaging

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestHandlePlatformMessageUsesPlatformReplier(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandlePlatformMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		AccountID: "bot-1",
		UserID:    "user-1",
		ChatID:    "user-1",
		MessageID: "9001",
		Text:      "/status",
	}, reply)

	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "agent:") {
		t.Fatalf("platform reply texts=%#v, want status reply", reply.Texts)
	}
}

func TestHandleMessageUsesPlatformDefaultAgent(t *testing.T) {
	codex := &fakeAgent{reply: "codex reply", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeAgent{reply: "claude reply", info: agent.AgentInfo{Name: "claude", Type: "test"}}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		switch name {
		case "claude":
			return claude
		case "codex":
			return codex
		default:
			return nil
		}
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetPlatformDefaultAgents(map[string]string{
		string(platform.PlatformFeishu): "claude",
	})

	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "user-1",
		Text:     "hello",
	}, reply)

	if !claude.wasChatCalled() {
		t.Fatal("claude was not called for feishu platform default agent")
	}
	if codex.chatCallCount() != 0 {
		t.Fatalf("codex calls=%d, want 0", codex.chatCallCount())
	}
}

func TestHandleMessageUsesFeishuAccountDefaultAgent(t *testing.T) {
	codex := &fakeAgent{reply: "codex reply", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeAgent{reply: "claude reply", info: agent.AgentInfo{Name: "claude", Type: "test"}}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		switch name {
		case "claude":
			return claude
		case "codex":
			return codex
		default:
			return nil
		}
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetPlatformDefaultAgents(map[string]string{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_b"): "claude",
	})

	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_b",
		UserID:    "user-1",
		Text:      "hello",
	}, reply)

	if !claude.wasChatCalled() {
		t.Fatal("claude was not called for feishu account default agent")
	}
	if codex.chatCallCount() != 0 {
		t.Fatalf("codex calls=%d, want 0", codex.chatCallCount())
	}
}

func TestHandleMessageUsesPersistedSessionDefaultAgent(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "agent-sessions.json")
	codex := &fakeAgent{reply: "codex reply", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeAgent{reply: "claude reply", info: agent.AgentInfo{Name: "claude", Type: "test"}}
	newHandler := func() *Handler {
		h := NewHandler(func(_ context.Context, name string) agent.Agent {
			switch name {
			case "claude":
				return claude
			case "codex":
				return codex
			default:
				return nil
			}
		}, nil)
		h.SetDefaultAgent("codex", codex)
		h.SetAgentMetas([]AgentMeta{{Name: "claude"}, {Name: "codex"}})
		h.SetPlatformDefaultAgents(map[string]string{
			PlatformAccountConfigKey(platform.PlatformFeishu, "cli_a"): "codex",
		})
		h.SetAgentSessionFile(stateFile)
		return h
	}
	sessionA := platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "user-1",
		MessageID: "switch-a",
		Text:      "/cc",
		Metadata:  map[string]string{"feishu_session_key": "feishu:tenant:dm:chat-a:user-1"},
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	newHandler().HandleMessage(context.Background(), sessionA, reply)
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "当前会话已切换到 claude") {
		t.Fatalf("switch reply=%#v", reply.Texts)
	}

	restored := newHandler()
	sessionA.MessageID = "message-a"
	sessionA.Text = "hello"
	restored.HandleMessage(context.Background(), sessionA, reply)
	if !claude.wasChatCalled() {
		t.Fatal("当前会话重启恢复后应调用 claude")
	}

	sessionB := sessionA
	sessionB.MessageID = "message-b"
	sessionB.Metadata = map[string]string{"feishu_session_key": "feishu:tenant:dm:chat-b:user-1"}
	restored.HandleMessage(context.Background(), sessionB, reply)
	waitForFakeAgentCalls(t, codex, 1)
	if codex.chatCallCount() != 1 {
		t.Fatalf("其他会话 codex 调用次数=%d，期望 1", codex.chatCallCount())
	}
}

func TestNamedAgentMessageDoesNotChangeSessionDefaultAgent(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "agent-sessions.json")
	codex := &fakeAgent{reply: "codex reply", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeAgent{reply: "claude reply", info: agent.AgentInfo{Name: "claude", Type: "test"}}
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "claude" {
			return claude
		}
		return codex
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetAgentMetas([]AgentMeta{{Name: "claude"}, {Name: "codex"}})
	if err := h.SetAgentSessionFile(stateFile); err != nil {
		t.Fatalf("设置状态文件失败：%v", err)
	}
	message := platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "user-1",
		MessageID: "named-message",
		Text:      "/cc 仅本次使用 Claude",
		Metadata:  map[string]string{"feishu_session_key": "feishu:tenant:dm:chat-a:user-1"},
	}
	h.HandleMessage(context.Background(), message, platformtest.NewReplier(platform.Capabilities{Text: true}))
	if _, ok := h.ensureAgentSessions().Get(platformMessageRouteUserID(message)); ok {
		t.Fatal("带内容的 Agent 命令不应修改会话默认 Agent")
	}
}

func TestAgentSwitchFailureDoesNotChangeSessionDefaultAgent(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "agent-sessions.json")
	h := NewHandler(func(context.Context, string) agent.Agent { return nil }, nil)
	h.SetAgentMetas([]AgentMeta{{Name: "claude"}})
	if err := h.SetAgentSessionFile(stateFile); err != nil {
		t.Fatalf("设置状态文件失败：%v", err)
	}
	message := platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "user-1",
		MessageID: "failed-switch",
		Text:      "/cc",
		Metadata:  map[string]string{"feishu_session_key": "feishu:tenant:dm:chat-a:user-1"},
	}
	h.HandleMessage(context.Background(), message, platformtest.NewReplier(platform.Capabilities{Text: true}))
	if _, ok := h.ensureAgentSessions().Get(platformMessageRouteUserID(message)); ok {
		t.Fatal("Agent 启动失败时不应修改会话默认 Agent")
	}
}

func TestHandleMessageUsesFeishuSessionMetadataForRouting(t *testing.T) {
	ag := &fakeAgent{reply: "ok", info: agent.AgentInfo{Name: "mock", Type: "test"}}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "mock" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("mock", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_sender",
		Text:     "hello",
		Metadata: map[string]string{"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root"},
	}, reply)

	if got := ag.lastChatConversationID(); got != "feishu:tenant_1:group:oc_1:om_root" {
		t.Fatalf("conversationID=%q, want feishu metadata session key", got)
	}
}

func TestHandleMessageKeepsFeishuChoiceLikeFinalReplyAsText(t *testing.T) {
	ag := &fakeAgent{
		reply: "请选择下一步：\n1. 确认计划\n2. 取消",
		info:  agent.AgentInfo{Name: "mock", Type: "test"},
	}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "mock" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("mock", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	sessionKey := "feishu:tenant_1:dm:oc_1:ou_sender"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_sender",
		Text:     "开始",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	if len(reply.Texts) != 1 || reply.Texts[0] != ag.reply {
		t.Fatalf("texts=%#v, want original final reply", reply.Texts)
	}
	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v, want no auto choice card", reply.Choices)
	}
}

func TestHandleMessageKeepsFeishuSenderUserIDForLogs(t *testing.T) {
	ag := &fakeAgent{reply: "ok", info: agent.AgentInfo{Name: "mock", Type: "test"}}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "mock" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("mock", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "hello",
		Metadata: map[string]string{"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root"},
	}, reply)

	output := logs.String()
	if !strings.Contains(output, "received from ou_user") {
		t.Fatalf("logs=%q, want true sender open_id", output)
	}
	if strings.Contains(output, "received from feishu:tenant_1:group:oc_1:om_root") {
		t.Fatalf("logs=%q, should not expose session key as sender", output)
	}
}

func TestHandleMessageKeepsFeishuSenderUserIDForWorkspaceCommands(t *testing.T) {
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply: "ok",
			info:  agent.AgentInfo{Name: "codex", Type: "test"},
		},
	}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("codex", ag)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	workspaceRoot := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspaceRoot})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/cwd " + workspaceRoot,
		Metadata: map[string]string{"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root"},
	}, reply)

	ownerWorkspace, ok := h.ensureCodexSessions().getActiveWorkspace(codexBindingKey("ou_user", "codex"))
	if !ok || ownerWorkspace != normalizeCodexWorkspaceRoot(workspaceRoot) {
		t.Fatalf("owner workspace=%q ok=%v, want %q for true sender", ownerWorkspace, ok, workspaceRoot)
	}
	if sessionWorkspace, ok := h.ensureCodexSessions().getActiveWorkspace(codexBindingKey("feishu:tenant_1:group:oc_1:om_root", "codex")); ok {
		t.Fatalf("session workspace=%q, should not bind /cwd to session key", sessionWorkspace)
	}
}
