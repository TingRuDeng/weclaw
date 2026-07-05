package messaging

import (
	"bytes"
	"context"
	"log"
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

func TestHandleMessageAttachesFeishuSessionMetadataToDetectedChoices(t *testing.T) {
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

	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v, want detected choices", reply.Choices)
	}
	for _, choice := range reply.Choices[0].Choices {
		if got := choice.Metadata["feishu_session_key"]; got != sessionKey {
			t.Fatalf("choice metadata=%#v, want session key %q", choice.Metadata, sessionKey)
		}
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
