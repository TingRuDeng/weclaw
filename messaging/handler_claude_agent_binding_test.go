package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuClaudeNewBindsWindowToClaude(t *testing.T) {
	h, _, claude, sessionKey := newClaudeBindingHandler(t)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "new-claude-session", Text: "/cc new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, platformtest.NewReplier(platform.Capabilities{Text: true}))

	if selected, ok := h.ensureAgentSessions().Get(sessionKey); !ok || selected != "claude" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望新建 Claude 会话后绑定 claude", selected, ok)
	}
	binding := h.ensureClaudeSessions().binding(claudeBindingKey(sessionKey, "claude"))
	if binding.SessionID != claude.resetSessionID || binding.Status != claudeBindingReady {
		t.Fatalf("binding=%+v，期望飞书窗口绑定新会话", binding)
	}
}

func TestHandleGlobalNewKeepsClaudeResetBehavior(t *testing.T) {
	h, codex, claude, sessionKey := newClaudeBindingHandler(t)
	if err := h.ensureAgentSessions().Set(sessionKey, "claude"); err != nil {
		t.Fatal(err)
	}
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "global-new-claude", Text: "/new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, platformtest.NewReplier(platform.Capabilities{Text: true}))

	if got := claude.resetConversationID(); !strings.Contains(got, sessionKey) {
		t.Fatalf("Claude reset conversation=%q，期望包含 route %q", got, sessionKey)
	}
	if got := codex.resetConversationID(); got != "" {
		t.Fatalf("Codex 不应被重置，实际 conversation=%q", got)
	}
	binding := h.ensureClaudeSessions().binding(claudeBindingKey(sessionKey, "claude"))
	if binding.SessionID != "session-new" || binding.Status != claudeBindingReady {
		t.Fatalf("binding=%+v，全局 /new 应绑定 Claude 会话", binding)
	}
}

func TestFeishuClaudeSessionSwitchBindsWindowToClaude(t *testing.T) {
	h, codex, claude, sessionKey := newClaudeBindingHandler(t)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "switch-claude-session", RawCommand: &platform.CardAction{
			Action: "choice", Value: map[string]string{"choice": "/cc switch session-claude"},
		},
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "message-after-switch", Text: "你是什么模型",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)
	waitUntil(t, claude.wasChatCalled)

	if selected, ok := h.ensureAgentSessions().Get(sessionKey); !ok || selected != "claude" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望成功切换后绑定 claude", selected, ok)
	}
	if !claude.wasChatCalled() {
		t.Fatal("切换 Claude 会话后的普通消息应发送给 Claude")
	}
	if codex.wasChatCalled() {
		t.Fatal("切换 Claude 会话后的普通消息不应发送给 Codex")
	}
}

func TestClaudeRuntimeFailureStillBindsCurrentWindowToClaude(t *testing.T) {
	h, _, claude, sessionKey := newClaudeBindingHandler(t)
	claude.useErr = errors.New("resume failed")
	if err := h.ensureAgentSessions().Set(sessionKey, "codex"); err != nil {
		t.Fatalf("设置初始窗口 Agent 失败：%v", err)
	}

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "failed-switch-claude", RawCommand: &platform.CardAction{
			Action: "choice", Value: map[string]string{"choice": "/cc switch session-claude"},
		},
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	if selected, ok := h.ensureAgentSessions().Get(sessionKey); !ok || selected != "claude" {
		t.Fatalf("窗口 Agent=(%q,%t)，session 绑定提交后应使用 claude", selected, ok)
	}
	if binding := h.ensureClaudeSessions().binding(claudeBindingKey(sessionKey, "claude")); binding.Status != claudeBindingResumeFailed {
		t.Fatalf("binding=%+v", binding)
	}
	if text := strings.Join(reply.Texts, "\n"); !strings.Contains(text, "绑定已保留") {
		t.Fatalf("reply=%q", text)
	}
	normalReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "message-after-runtime-failure", Text: "继续",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, normalReply)
	if claude.wasChatCalled() || len(normalReply.Choices) != 0 {
		t.Fatalf("runtime 不可用时不应写入或重选 owner: chat=%t choices=%#v", claude.wasChatCalled(), normalReply.Choices)
	}
	if text := strings.Join(normalReply.Texts, "\n"); !strings.Contains(text, "ClaudeHost 暂不可用") || !strings.Contains(text, "绑定不会被释放") {
		t.Fatalf("normal reply=%q", text)
	}
}

func TestCodexSessionSwitchBindsWindowToCodex(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	if err := h.ensureAgentSessions().Set("user-1", "claude"); err != nil {
		t.Fatalf("设置初始窗口 Agent 失败：%v", err)
	}

	h.handleCodexSwitchForRouteWithOptions(codexSwitchRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex",
		workspaceRoot: workspace, agent: ag, target: "thread-1",
		options: codexSwitchOptions{actorUserID: "user-1"},
	})

	if selected, ok := h.ensureAgentSessions().Get("user-1"); !ok || selected != "codex" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望成功切换后绑定 codex", selected, ok)
	}
}

func TestCodexSessionNewBindsWindowToCodex(t *testing.T) {
	workspace := t.TempDir()
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	h := NewHandler(nil, nil)
	if err := h.ensureAgentSessions().Set("user-1", "claude"); err != nil {
		t.Fatalf("设置初始窗口 Agent 失败：%v", err)
	}

	h.handleCodexNewForRoute(codexNewRequest{
		ctx: context.Background(), actorUserID: "user-1", userID: "user-1",
		agentName: "codex", workspaceRoot: workspace, agent: ag,
	})

	if selected, ok := h.ensureAgentSessions().Get("user-1"); !ok || selected != "codex" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望新建 Codex 会话后绑定 codex", selected, ok)
	}
}

func newClaudeBindingHandler(t *testing.T) (*Handler, *fakeAgent, *fakeClaudeSessionAgent, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	codex := &fakeAgent{reply: "codex", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeClaudeSessionAgent{
		fakeAgent:       fakeAgent{reply: "claude", resetSessionID: "session-new", info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}},
		catalogSessions: []agent.ClaudeSession{{ID: "session-claude", Cwd: workspace, Title: "Claude 会话"}},
	}
	h := NewHandler(func(_ context.Context, name string) agent.Agent {
		if name == "claude" {
			return claude
		}
		return codex
	}, nil)
	h.SetDefaultAgent("codex", codex)
	h.SetAgentMetas([]AgentMeta{{Name: "claude"}, {Name: "codex"}})
	h.SetPlatformDefaultAgents(map[string]string{
		PlatformAccountConfigKey(platform.PlatformFeishu, "cli_android"): "codex",
	})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	return h, codex, claude, "feishu:tenant:dm:chat:ou_user"
}
