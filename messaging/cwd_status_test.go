package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCwdCommandDetectionRequiresExactToken(t *testing.T) {
	for _, command := range []string{"/cwd", "/cwd /path/with spaces"} {
		if !isCwdCommand(command) {
			t.Fatalf("%q should be a cwd command", command)
		}
	}
	for _, message := range []string{"/cwdfoo", "/cwd-list", "cwd"} {
		if isCwdCommand(message) {
			t.Fatalf("%q must not be captured as a cwd command", message)
		}
	}
	if got := NewHandler(nil, nil).handleCwd("/cwdfoo"); got != "用法: /cwd [路径]" {
		t.Fatalf("direct invalid cwd reply=%q", got)
	}
}

func TestCwdStatusUsesFeishuRouteCodexWorkspaceWithoutMutation(t *testing.T) {
	ag := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "codex", Type: "acp", Command: "codex",
	}}}
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("codex", ag)
	fallback := canonicalTestPath(t, t.TempDir())
	workspace := canonicalTestPath(t, t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": fallback})
	route := "feishu:tenant:dm:chat-a:user-a"
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(route, "codex"), workspace)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "user-a",
		Text:     "/cwd",
		Metadata: map[string]string{"feishu_session_key": route},
	}, reply)

	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "cwd: "+workspace) ||
		!strings.Contains(reply.Texts[0], "agent: codex") {
		t.Fatalf("cwd reply=%#v, want route workspace %q", reply.Texts, workspace)
	}
	if got := ag.lastWorkingDir(); got != "" {
		t.Fatalf("query changed runtime cwd to %q", got)
	}
	h.mu.RLock()
	gotFallback := h.agentWorkDirs["codex"]
	h.mu.RUnlock()
	if gotFallback != fallback {
		t.Fatalf("query changed global cwd=%q, want %q", gotFallback, fallback)
	}
}

func TestCwdStatusUsesFeishuRouteClaudeWorkspaceWithoutMutation(t *testing.T) {
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
		Name: "claude", Type: "acp", Command: "claude-agent-acp",
	}}}
	h := NewHandler(nil, nil)
	h.SetDefaultAgent("claude", ag)
	fallback := canonicalTestPath(t, t.TempDir())
	workspace := canonicalTestPath(t, t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"claude": fallback})
	route := "feishu:tenant:dm:chat-a:user-a"
	key := claudeBindingKey(route, "claude")
	h.ensureClaudeSessions().bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "user-a",
		Text:     "/cwd",
		Metadata: map[string]string{"feishu_session_key": route},
	}, reply)

	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "cwd: "+workspace) ||
		!strings.Contains(reply.Texts[0], "agent: claude") {
		t.Fatalf("cwd reply=%#v, want route workspace %q", reply.Texts, workspace)
	}
	if got := ag.lastWorkingDir(); got != "" {
		t.Fatalf("query changed runtime cwd to %q", got)
	}
}
