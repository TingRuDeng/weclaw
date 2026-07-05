package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuNewUsesSessionMetadataForReset(t *testing.T) {
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply:          "ok",
			resetSessionID: "thread-new",
			info:           agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("codex", ag)
	h.SetCodexLocalSessionDir(t.TempDir())
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	if got := ag.resetConversationID(); !strings.Contains(got, sessionKey) {
		t.Fatalf("reset conversation=%q, want route session key %q", got, sessionKey)
	}
	routeThread, pending := h.ensureCodexSessions().getThread(codexBindingKey(sessionKey, "codex"), h.codexWorkspaceRootForUser("ou_user", "codex", ag))
	if routeThread != "thread-new" || pending {
		t.Fatalf("route thread=%q pending=%v, want thread-new false", routeThread, pending)
	}
}

func TestFeishuCodexStatusUsesSessionMetadataForRouting(t *testing.T) {
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-route",
	}
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("codex", ag)
	h.SetCodexLocalSessionDir(t.TempDir())
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/cx status",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	routeThread, pending := h.ensureCodexSessions().getThread(codexBindingKey(sessionKey, "codex"), h.codexWorkspaceRootForUser("ou_user", "codex", ag))
	if routeThread != "thread-route" || pending {
		t.Fatalf("route thread=%q pending=%v, want thread-route false", routeThread, pending)
	}
	ownerThread, _ := h.ensureCodexSessions().getThread(codexBindingKey("ou_user", "codex"), h.codexWorkspaceRootForUser("ou_user", "codex", ag))
	if ownerThread != "" {
		t.Fatalf("owner thread=%q, should not bind /cx status to true sender", ownerThread)
	}
}

func TestFeishuHelpChoicesCarrySessionMetadata(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/help",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v, want help choice card", reply.Choices)
	}
	for _, choice := range reply.Choices[0].Choices {
		if choice.Metadata["feishu_session_key"] != sessionKey {
			t.Fatalf("choice=%#v, want feishu session metadata %q", choice, sessionKey)
		}
	}
}

func TestFeishuRawCommandStopUsesSessionMetadata(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"},
		},
	}
	h.agents["codex"] = ag
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey("ou_user", "codex"), t.TempDir())
	key := h.agentExecutionKeyForRoute("ou_user", sessionKey, "codex", ag)
	task, taskCtx, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{owner: "ou_user", agentName: "codex", message: "hi"})
	if !started {
		t.Fatal("beginActiveTask started=false, want true")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		RawCommand: &platform.CardAction{
			Action: "stop",
		},
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("task context was not canceled by route session key")
	}
	h.finishActiveTask(key, task)
}

func TestFeishuRunPendingUsesSessionMetadata(t *testing.T) {
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			reply: "ok",
			info:  agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
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
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey("ou_user", "codex"), t.TempDir())
	executionKey := h.agentExecutionKeyForRoute("ou_user", sessionKey, "codex", ag)
	h.storePendingCodexRun(executionKey, "继续执行")

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/run",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	waitUntil(t, func() bool { return ag.lastChatMessage() == "继续执行" })
	if got := ag.lastChatMessage(); got != "继续执行" {
		t.Fatalf("chat message=%q, want pending message from route session", got)
	}
	if got := ag.lastChatConversationID(); !strings.Contains(got, sessionKey) {
		t.Fatalf("conversation=%q, want route session key %q", got, sessionKey)
	}
}

func TestPlatformMessageSessionKeyIgnoresWechatMetadata(t *testing.T) {
	msg := platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "wx_user",
		Metadata: map[string]string{"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root"},
	}

	if got := platformMessageSessionKey(msg); got != "" {
		t.Fatalf("wechat session key=%q, want empty", got)
	}
}
