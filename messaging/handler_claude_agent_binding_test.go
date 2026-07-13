package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuClaudeNewBindsWindowToClaude(t *testing.T) {
	h, _, _, sessionKey := newClaudeBindingHandler(t)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "new-claude-session", Text: "/cc new",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, platformtest.NewReplier(platform.Capabilities{Text: true}))

	if selected, ok := h.ensureAgentSessions().Get(sessionKey); !ok || selected != "claude" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望新建 Claude 会话后绑定 claude", selected, ok)
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

func TestFailedClaudeSessionSwitchKeepsCurrentWindowAgent(t *testing.T) {
	h, _, claude, sessionKey := newClaudeBindingHandler(t)
	claude.useErr = errors.New("resume failed")
	if err := h.ensureAgentSessions().Set(sessionKey, "codex"); err != nil {
		t.Fatalf("设置初始窗口 Agent 失败：%v", err)
	}

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_android", UserID: "ou_user",
		MessageID: "failed-switch-claude", RawCommand: &platform.CardAction{
			Action: "choice", Value: map[string]string{"choice": "/cc switch session-claude"},
		},
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true}))

	if selected, ok := h.ensureAgentSessions().Get(sessionKey); !ok || selected != "codex" {
		t.Fatalf("窗口 Agent=(%q,%t)，失败切换不应覆盖原 codex 绑定", selected, ok)
	}
}

func TestCodexSessionSwitchBindsWindowToCodex(t *testing.T) {
	h, ag, workspace := codexLiveSwitchFixture(t, agent.CodexThreadState{ThreadID: "thread-1"})
	if err := h.ensureAgentSessions().Set("user-1", "claude"); err != nil {
		t.Fatalf("设置初始窗口 Agent 失败：%v", err)
	}

	h.handleCodexSwitchForRouteWithOptions(
		context.Background(), "user-1", "codex", workspace, ag, "thread-1", "",
		codexSwitchOptions{actorUserID: "user-1"},
	)

	if selected, ok := h.ensureAgentSessions().Get("user-1"); !ok || selected != "codex" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望成功切换后绑定 codex", selected, ok)
	}
}

func TestCodexSessionNewBindsWindowToCodex(t *testing.T) {
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}, resetSessionID: "thread-new",
	}}
	h := NewHandler(nil, nil)
	if err := h.ensureAgentSessions().Set("user-1", "claude"); err != nil {
		t.Fatalf("设置初始窗口 Agent 失败：%v", err)
	}

	h.handleCodexNewForRoute(codexNewRequest{
		ctx: context.Background(), userID: "user-1", agentName: "codex", workspaceRoot: workspace, agent: ag,
	})

	if selected, ok := h.ensureAgentSessions().Get("user-1"); !ok || selected != "codex" {
		t.Fatalf("窗口 Agent=(%q,%t)，期望新建 Codex 会话后绑定 codex", selected, ok)
	}
}

func newClaudeBindingHandler(t *testing.T) (*Handler, *fakeAgent, *fakeClaudeSessionAgent, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "project")
	claudeDir := t.TempDir()
	writeLocalClaudeSession(t, claudeDir, "session-claude", workspace, "Claude 会话", "2026-07-13T07:00:00Z")
	codex := &fakeAgent{reply: "codex", info: agent.AgentInfo{Name: "codex", Type: "test"}}
	claude := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{
		reply: "claude", info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
	}}
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
	h.SetClaudeLocalSessionDir(claudeDir)
	return h, codex, claude, "feishu:tenant:dm:chat:ou_user"
}
