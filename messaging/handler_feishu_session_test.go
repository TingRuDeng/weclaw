package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuGroupStatusUsesChatSessionMetadataForRouting(t *testing.T) {
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
	sessionKey := "feishu:tenant_1:group:oc_1"

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

func TestCodexNewThreadIsNotBuiltinSessionCommand(t *testing.T) {
	if isCodexSessionCommand("/cx new-thread") {
		t.Fatal("/cx new-thread should not be treated as a builtin Codex session command")
	}
}

func TestFeishuDMSessionWorkspaceSwitchStaysInChatSession(t *testing.T) {
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	h := NewHandler(func(ctx context.Context, name string) agent.Agent {
		if name == "codex" {
			return ag
		}
		return nil
	}, nil)
	h.SetDefaultAgent("codex", ag)
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetDefaultAgent("codex", ag)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T10:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)

	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	route := "feishu:tenant_1:dm:oc_1:ou_user"

	h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "ou_user",
		RouteUserID: route,
		Trimmed:     "/cx cd beta",
		Platform:    platform.PlatformFeishu,
		Reply:       platformtest.NewReplier(platform.Capabilities{Text: true}),
	})
	status := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "ou_user",
		RouteUserID: route,
		Trimmed:     "/cx status",
		Platform:    platform.PlatformFeishu,
		Reply:       platformtest.NewReplier(platform.Capabilities{Text: true}),
	})

	if !strings.Contains(status, "工作空间: "+workspaceB) {
		t.Fatalf("route status=%q, want workspace B after chat session switch", status)
	}

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "执行一个任务",
		Metadata: map[string]string{"feishu_session_key": route},
	}, platformtest.NewReplier(platform.Capabilities{Text: true}))
	waitForFakeAgentCalls(t, &ag.fakeAgent, 1)
	wantConversationID := buildCodexConversationID(route, "codex", workspaceB)
	if got := ag.lastChatConversationID(); got != wantConversationID {
		t.Fatalf("route B conversation=%q, want %q", got, wantConversationID)
	}
}

func TestFeishuCwdUsesChatSessionMetadataForClaudeBinding(t *testing.T) {
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "claude", Type: "acp", Command: "claude-agent-acp",
	}}}
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("claude", ag)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant_1:dm:oc_1:ou_user"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user",
		Text:     "/cwd " + workspace,
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	routeBinding := h.ensureClaudeSessions().binding(claudeBindingKey(sessionKey, "claude"))
	if routeBinding.WorkspaceRoot != canonicalTestPath(t, workspace) {
		t.Fatalf("route binding=%+v，期望飞书窗口绑定工作空间 %q", routeBinding, workspace)
	}
	actorBinding := h.ensureClaudeSessions().binding(claudeBindingKey("ou_user", "claude"))
	if actorBinding.WorkspaceRoot != "" {
		t.Fatalf("actor binding=%+v，不应把飞书 /cwd 写入裸用户兼容绑定", actorBinding)
	}
}

func TestFeishuHelpChoicesCarrySessionMetadata(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	sessionKey := "feishu:tenant_1:group:oc_1"

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
	sessionKey := "feishu:tenant_1:group:oc_1"
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

func TestFeishuGroupTaskRejectsStopAndCancelFromOtherUser(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"},
	}}
	h.agents["codex"] = ag
	sessionKey := "feishu:tenant_1:group:oc_1"
	key := h.agentExecutionKeyForRoute("ou_owner", sessionKey, "codex", ag)
	task, taskCtx, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{
		owner: "ou_owner", agentName: "codex", message: "第一条",
	})
	if !started || !h.storePendingGuide(key, pendingAgentTask{message: "第二条", run: func() {}}) {
		t.Fatal("准备运行中任务失败")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	for _, command := range []string{"/cancel", "/stop"} {
		h.HandleMessage(context.Background(), platform.IncomingMessage{
			Platform: platform.PlatformFeishu,
			UserID:   "ou_other",
			Text:     command,
			Metadata: map[string]string{"feishu_session_key": sessionKey},
		}, reply)
	}

	select {
	case <-taskCtx.Done():
		t.Fatal("其他群成员不应停止任务发起人的任务")
	default:
	}
	if got := task.pendingGuide(); got != "第二条" {
		t.Fatalf("pending guide=%q, want unchanged", got)
	}
	if !containsText(reply.Texts, "只有任务发起人可以") {
		t.Fatalf("reply texts=%#v, want owner rejection", reply.Texts)
	}
	h.finishActiveTask(key, task)
}

func TestFeishuPendingMessageRunsAutomaticallyInOriginalSession(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := newBlockingProgressAgent()
	h.SetDefaultAgent("codex", ag)
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	sessionKey := "feishu:tenant_1:group:oc_1"
	message := func(text string) platform.IncomingMessage {
		return platform.IncomingMessage{
			Platform: platform.PlatformFeishu, UserID: "ou_user", Text: text,
			Metadata: map[string]string{"feishu_session_key": sessionKey},
		}
	}

	h.HandleMessage(context.Background(), message("第一条"), reply)
	waitForAgentEnter(t, ag)
	h.HandleMessage(context.Background(), message("第二条"), reply)
	ag.release <- struct{}{}
	waitForAgentEnter(t, ag)

	key := h.agentExecutionKeyForRoute("ou_user", sessionKey, "codex", ag)
	if _, ok := h.activeTask(key); !ok {
		t.Fatal("自动续跑任务应保持在原飞书 session")
	}
	ag.release <- struct{}{}
	waitUntil(t, func() bool {
		_, ok := h.activeTask(key)
		return !ok
	})
}

func TestPlatformMessageSessionKeyIgnoresWechatMetadata(t *testing.T) {
	msg := platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "wx_user",
		Metadata: map[string]string{"feishu_session_key": "feishu:tenant_1:group:oc_1"},
	}

	if got := platformMessageSessionKey(msg); got != "" {
		t.Fatalf("wechat session key=%q, want empty", got)
	}
}
