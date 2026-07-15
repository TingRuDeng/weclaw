package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestClaudeOwnerQueryDoesNotChangeControl(t *testing.T) {
	h, _, workspace := newClaudeACPNavigationHandler(t)
	key := seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-a", 4)
	before := h.ensureClaudeSessions().controlIntent("session-a")

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner")
	after := h.ensureClaudeSessions().controlIntent("session-a")
	if before != after || !strings.Contains(text, "当前远程窗口") || !strings.Contains(text, "session-a") {
		t.Fatalf("text=%q before=%+v after=%+v key=%q", text, before, after, key)
	}
}

func TestClaudeOwnerLocalBlocksNormalMessagesUntilRemoteReacquire(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-a", 1)
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-a", Cwd: workspace}}

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
	if !strings.Contains(text, "已释放") || h.ensureClaudeSessions().controlIntent("session-a").Owner != claudeOwnerLocal {
		t.Fatalf("text=%q intent=%+v", text, h.ensureClaudeSessions().controlIntent("session-a"))
	}
	local := h.ensureClaudeSessions().controlIntent("session-a")
	text = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
	if !strings.Contains(text, "已释放") || h.ensureClaudeSessions().controlIntent("session-a") != local {
		t.Fatalf("idempotent local text=%q before=%+v after=%+v", text, local, h.ensureClaudeSessions().controlIntent("session-a"))
	}
	_, err := h.resolveAgentConversationIDForRoute(context.Background(), "user-1", "user-1", "claude", fake)
	if err == nil || !strings.Contains(err.Error(), "/cc owner remote") {
		t.Fatalf("error=%v", err)
	}
	text = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner remote")
	if !strings.Contains(text, "已接管") || h.ensureClaudeSessions().controlIntent("session-a").Owner != claudeOwnerRemote {
		t.Fatalf("text=%q intent=%+v", text, h.ensureClaudeSessions().controlIntent("session-a"))
	}
	remote := h.ensureClaudeSessions().controlIntent("session-a")
	useCalls := len(fake.useCalls)
	text = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner remote")
	if !strings.Contains(text, "已接管") || h.ensureClaudeSessions().controlIntent("session-a") != remote || len(fake.useCalls) != useCalls {
		t.Fatalf("idempotent remote text=%q before=%+v after=%+v useCalls=%#v", text, remote, h.ensureClaudeSessions().controlIntent("session-a"), fake.useCalls)
	}
}

func TestClaudeOwnerLocalRejectsOtherRouteAndActiveTask(t *testing.T) {
	h, _, workspace := newClaudeACPNavigationHandler(t)
	requestKey := claudeBindingKey("user-1", "claude")
	ownerKey := claudeBindingKey("route-owner", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[requestKey] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: ownerKey,
		ConversationID: buildClaudeConversationID("route-owner", "claude", workspace), Revision: 2,
	}
	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
	if !strings.Contains(text, "其他远程窗口") || store.controlIntent("session-a").BindingKey != ownerKey {
		t.Fatalf("text=%q intent=%+v", text, store.controlIntent("session-a"))
	}

	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store.controls["session-a"] = claudeControlIntent{Owner: claudeOwnerRemote, BindingKey: requestKey, ConversationID: conversationID, Revision: 3}
	task, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{owner: "user-1"})
	if !started {
		t.Fatal("准备活动任务失败")
	}
	defer h.finishActiveTask(conversationID, task)
	text = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
	if !strings.Contains(text, "任务") || store.controlIntent("session-a").Owner != claudeOwnerRemote {
		t.Fatalf("text=%q intent=%+v", text, store.controlIntent("session-a"))
	}
}

func TestClaudeOtherRouteOwnerBlocksTaskAdmission(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	requestKey := claudeBindingKey("route-request", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[requestKey] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: claudeBindingKey("route-owner", "claude"),
		ConversationID: "owner-conversation", Revision: 1,
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.startAgentTask(agentTaskOptions{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "actor", routeUserID: "route-request", reply: reply,
		agentName: "claude", message: "blocked", agent: fake,
		progressCfg: config.DefaultProgressConfig(),
	})
	if h.ActiveTaskCount() != 0 || !containsText(reply.Texts, "其他远程窗口") {
		t.Fatalf("active=%d texts=%#v", h.ActiveTaskCount(), reply.Texts)
	}
}

func TestClaudeTaskControlRevisionChangePreventsPrompt(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	workspace := h.claudeWorkspaceRoot("claude")
	key := seedClaudeRemoteControl(t, h, "route-1", "claude", workspace, "session-a", 1)
	conversationID := buildClaudeConversationID("route-1", "claude", workspace)
	unblock := h.lockAgentExecution(conversationID)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.startAgentTask(agentTaskOptions{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "user-1", routeUserID: "route-1", reply: reply,
		agentName: "claude", message: "must not prompt", agent: ag,
		progressCfg: config.DefaultProgressConfig(),
	})
	store := h.ensureClaudeSessions()
	store.mu.Lock()
	intent := store.controls["session-a"]
	intent.Revision++
	store.controls["session-a"] = intent
	store.mu.Unlock()
	unblock()
	waitUntil(t, func() bool { return h.ActiveTaskCount() == 0 })
	started, _ := ag.stats()
	if started != 0 || !containsText(reply.Texts, "状态刚刚发生变化") {
		t.Fatalf("key=%q started=%d texts=%#v", key, started, reply.Texts)
	}
}

func TestClaudeModelWriteRequiresCurrentRemoteOwner(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent claudeControlIntent
		want   string
	}{
		{name: "local", intent: claudeControlIntent{Owner: claudeOwnerLocal, Revision: 2}, want: "/cc owner remote"},
		{name: "other remote", intent: claudeControlIntent{
			Owner: claudeOwnerRemote, BindingKey: claudeBindingKey("other", "claude"),
			ConversationID: "other-conversation", Revision: 3,
		}, want: "其他远程窗口"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ag := &fakeCurrentClaudeModelAgent{fakeClaudeModelAgent: fakeClaudeModelAgent{
				fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp"}},
			}, config: agent.ClaudeSessionConfig{Model: "sonnet"}}
			h := NewHandler(nil, nil)
			workspace := t.TempDir()
			key := claudeBindingKey("route-model", "claude")
			store := h.ensureClaudeSessions()
			store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
			store.controls["session-a"] = test.intent

			text, handled := h.setCurrentClaudeSessionSetting(claudeModelSettingRequest{
				ctx: context.Background(), route: modelAgentRoute{routeUserID: "route-model"},
				name: "claude", agent: ag, model: "opus",
			})
			if !handled || !strings.Contains(text, test.want) || len(ag.updates) != 0 {
				t.Fatalf("handled=%v text=%q updates=%#v", handled, text, ag.updates)
			}
		})
	}
}

func TestClaudeModelWriteRejectsInconsistentConversation(t *testing.T) {
	ag := &fakeCurrentClaudeModelAgent{fakeClaudeModelAgent: fakeClaudeModelAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp"}},
	}, config: agent.ClaudeSessionConfig{Model: "sonnet"}}
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	key := claudeBindingKey("route-model", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "wrong-conversation", Revision: 1,
	}
	text, handled := h.setCurrentClaudeSessionSetting(claudeModelSettingRequest{
		ctx: context.Background(), route: modelAgentRoute{routeUserID: "route-model"},
		name: "claude", agent: ag, model: "opus",
	})
	if !handled || !strings.Contains(text, "不一致") || len(ag.updates) != 0 {
		t.Fatalf("handled=%v text=%q updates=%#v", handled, text, ag.updates)
	}
}

func TestClaudeRequireRemoteControlFailsClosedOnInconsistentState(t *testing.T) {
	workspace := t.TempDir()
	key := claudeBindingKey("route-control", "claude")
	wantConversation := buildClaudeConversationID("route-control", "claude", workspace)
	for _, test := range []struct {
		name     string
		binding  claudeSessionBinding
		controls map[string]claudeControlIntent
		want     string
	}{
		{
			name:     "unbound",
			binding:  newClaudeBinding(workspace, "", claudeBindingUnbound),
			controls: map[string]claudeControlIntent{},
			want:     "/cc ls",
		},
		{
			name:    "resume failed",
			binding: newClaudeBinding(workspace, "session-a", claudeBindingResumeFailed),
			controls: map[string]claudeControlIntent{"session-a": {
				Owner: claudeOwnerRemote, BindingKey: key, ConversationID: wantConversation, Revision: 1,
			}},
			want: "重新选择",
		},
		{
			name:    "conversation mismatch",
			binding: newClaudeBinding(workspace, "session-a", claudeBindingReady),
			controls: map[string]claudeControlIntent{"session-a": {
				Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "wrong-conversation", Revision: 1,
			}},
			want: "重新选择",
		},
		{
			name:    "workspace mismatch",
			binding: newClaudeBinding(t.TempDir(), "session-a", claudeBindingReady),
			controls: map[string]claudeControlIntent{"session-a": {
				Owner: claudeOwnerRemote, BindingKey: key, ConversationID: wantConversation, Revision: 1,
			}},
			want: "重新选择",
		},
		{
			name:    "session mismatch",
			binding: newClaudeBinding(workspace, "session-a", claudeBindingReady),
			controls: map[string]claudeControlIntent{"session-b": {
				Owner: claudeOwnerRemote, BindingKey: key, ConversationID: wantConversation, Revision: 1,
			}},
			want: "重新选择",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newClaudeSessionStore()
			store.bindings[key] = test.binding
			store.controls = test.controls
			_, _, err := store.requireRemoteControl(key)
			if err == nil || !strings.Contains(renderClaudeRemoteControlError(err), test.want) {
				t.Fatalf("error=%v text=%q", err, renderClaudeRemoteControlError(err))
			}
		})
	}
}

func TestClaudeRequireRemoteControlAllowsPendingResume(t *testing.T) {
	store := newClaudeSessionStore()
	workspace := t.TempDir()
	key := claudeBindingKey("route-pending", "claude")
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingPendingResume)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("route-pending", "claude", workspace), Revision: 2,
	}
	if _, _, err := store.requireRemoteControl(key); err != nil {
		t.Fatalf("pending_resume should be admissible: %v", err)
	}
}

func TestClaudeInconsistentControlBlocksTaskAdmission(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("route-inconsistent", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: "wrong-conversation", Revision: 1,
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.startAgentTask(agentTaskOptions{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "actor", routeUserID: "route-inconsistent", reply: reply,
		agentName: "claude", message: "blocked", agent: fake,
		progressCfg: config.DefaultProgressConfig(),
	})
	if h.ActiveTaskCount() != 0 || !containsText(reply.Texts, "不一致") {
		t.Fatalf("active=%d texts=%#v", h.ActiveTaskCount(), reply.Texts)
	}
}

func seedClaudeRemoteControl(t *testing.T, h *Handler, routeUserID, agentName, workspace, sessionID string, revision uint64) string {
	t.Helper()
	key := claudeBindingKey(routeUserID, agentName)
	conversationID := buildClaudeConversationID(routeUserID, agentName, workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, sessionID, claudeBindingReady)
	store.controls[sessionID] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: revision,
	}
	return key
}
