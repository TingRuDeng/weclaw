package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuNewUsesGroupSessionMetadataForReset(t *testing.T) {
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("codex", ag)
	h.SetCodexLocalSessionDir(t.TempDir())
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant_1:group:oc_1"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", Text: "/new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	_, resetConversation := ag.resetSnapshot()
	if !strings.Contains(resetConversation, sessionKey) {
		t.Fatalf("reset conversation=%q, want route session key %q", resetConversation, sessionKey)
	}
	bindingKey := codexBindingKey(sessionKey, "codex")
	workspace := h.codexWorkspaceRootForUser("ou_user", "codex", ag)
	routeThread, pending := h.ensureCodexSessions().getThread(bindingKey, workspace)
	if routeThread != "thread-new" || pending {
		t.Fatalf("route thread=%q pending=%v, want thread-new false", routeThread, pending)
	}
	requests := ag.handoffRequests()
	if len(requests) != 1 || requests[0].Intent.RouteKey != bindingKey {
		t.Fatalf("shared host bind requests=%#v", requests)
	}
}

func TestHandleGlobalNewPassesFeishuObserverContext(t *testing.T) {
	state := agent.CodexThreadState{ThreadID: "thread-new", Active: true, ActiveTurnID: "turn-new"}
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeWeClaw, state)
	ag.resetSessionID = "thread-new"
	ag.watchDone = make(chan struct{})
	h := NewHandler(func(_ context.Context, _ string) agent.Agent { return ag }, nil)
	h.SetDefaultAgent("codex", ag)
	workspace := t.TempDir()
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant:dm:chat-new:ou_actor"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_actor",
		MessageID: "global-new-context", Text: "/new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	conversationID := buildCodexConversationID(sessionKey, "codex", workspace)
	task, active := h.activeTask(conversationID)
	if !active {
		t.Fatal("活动新会话未建立飞书观察任务")
	}
	task.mu.Lock()
	control := task.externalReservation
	owner, route := task.owner, task.routeUserID
	task.mu.Unlock()
	control.mu.Lock()
	opts := control.runtime.opts
	control.mu.Unlock()
	if owner != "ou_actor" || route != sessionKey || opts.platform != platform.PlatformFeishu ||
		opts.accountID != "cli_android" || opts.reply != reply {
		t.Fatalf("观察上下文 owner=%q route=%q platform=%q account=%q replyMatch=%v", owner, route, opts.platform, opts.accountID, opts.reply == reply)
	}
	close(ag.watchDone)
	waitUntil(t, func() bool { _, active := h.activeTask(conversationID); return !active })
}

func TestFeishuNewUsesSessionDefaultAgent(t *testing.T) {
	workspace := t.TempDir()
	codex := &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}, resetSessionID: "thread-new",
	}}
	claude := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}, resetSessionID: "session-new",
	}}
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "claude" {
			return claude
		}
		return codex
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetAgentMetas([]AgentMeta{{Name: "claude"}, {Name: "codex"}})
	h.SetAgentWorkDirs(map[string]string{"claude": workspace, "codex": workspace})
	sessionKey := "feishu:tenant:dm:chat-a:user-1"
	if err := h.ensureAgentSessions().Set(sessionKey, "claude"); err != nil {
		t.Fatalf("设置会话 Agent 失败：%v", err)
	}

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_main", UserID: "user-1",
		MessageID: "new-session-agent", Text: "/new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, platformtest.NewReplier(platform.Capabilities{Text: true}))

	want := buildClaudeConversationID(sessionKey, "claude", workspace)
	if got := claude.resetConversationID(); got != want {
		t.Fatalf("Claude reset conversation=%q，期望 %q", got, want)
	}
	if got := codex.resetConversationID(); got != "" {
		t.Fatalf("Codex 不应被重置，实际 conversation=%q", got)
	}
	intent := h.ensureClaudeSessions().controlIntent("session-new")
	if intent.Owner != claudeOwnerRemote || intent.BindingKey != claudeBindingKey(sessionKey, "claude") {
		t.Fatalf("intent=%+v，会话默认 Claude 的 /new 应接管新会话", intent)
	}
}
