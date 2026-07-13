package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuClaudeCcLsSendsAllowedACPWorkspaceChoices(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	allowedRoot := t.TempDir()
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-a", Cwd: filepath.Join(allowedRoot, "alpha"), Title: "Alpha 会话"},
		{ID: "session-b", Cwd: filepath.Join(allowedRoot, "beta"), Title: "Beta 会话"},
		{ID: "blocked", Cwd: t.TempDir(), Title: "越权会话"},
	}
	for _, session := range ag.catalogSessions[:2] {
		if err := os.MkdirAll(session.Cwd, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.SetAllowedWorkspaceRoots([]string{allowedRoot})
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc ls"})

	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v texts=%#v，期望两个已授权 ACP 工作空间", reply.Choices, reply.Texts)
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts=%#v，卡片成功后不应重复文本", reply.Texts)
	}
}

func TestFeishuClaudeWorkspaceChoiceSendsStableSessionChoices(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-new", Cwd: workspace, Title: "较新会话", UpdatedAt: "2026-07-13T10:00:00Z"},
		{ID: "session-old", Cwd: workspace, Title: "较早会话", UpdatedAt: "2026-07-13T09:00:00Z"},
	}
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: "/cc cd 0"})

	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 3 {
		t.Fatalf("choices=%#v，期望会话与返回按钮", reply.Choices)
	}
	if reply.Choices[0].Choices[0].ID != "/cc switch session-new" {
		t.Fatalf("choices=%#v，期望稳定 sessionId", reply.Choices)
	}
	binding := h.ensureClaudeSessions().binding(claudeBindingKey("feishu:user", "claude"))
	if binding.WorkspaceRoot != workspace || binding.Status != claudeBindingUnbound {
		t.Fatalf("binding=%+v，进入工作空间后应等待显式选择", binding)
	}
}

func TestFeishuClaudeCcLsAllowsAdminOutsideRoots(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	h.SetAdminUsers([]string{"on_admin"})
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-a", Cwd: filepath.Join(t.TempDir(), "alpha")},
		{ID: "session-b", Cwd: filepath.Join(t.TempDir(), "beta")},
	}
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{
		Handler: h, SessionKey: "feishu:admin", Text: "/cc ls", UnionID: "on_admin",
	})
	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v，管理员应看到白名单外工作空间", reply.Choices)
	}
}

func TestFeishuClaudeInvalidWorkspaceReturnsCcGuidance(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag.catalogSessions = []agent.ClaudeSession{{ID: "session-a", Cwd: workspace}}
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc cd missing"})

	if len(reply.Choices) != 0 || len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "/cc ls") {
		t.Fatalf("choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
}

func TestFeishuClaudeCcLsDuringActiveTaskDoesNotSendCard(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag.catalogSessions = []agent.ClaudeSession{{ID: "session-a", Cwd: workspace}}
	key := h.agentExecutionKeyForRoute("user", "feishu:user", "claude", ag)
	task, _, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{owner: "user", agentName: "claude"})
	if !started {
		t.Fatal("Claude active task should start")
	}
	defer h.finishActiveTask(key, task)
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc ls"})
	if len(reply.Choices) != 0 || len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "正在执行") {
		t.Fatalf("choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
}

func newClaudeFeishuCardHandler(t *testing.T) (*Handler, *fakeClaudeSessionAgent) {
	t.Helper()
	h := NewHandler(nil, nil)
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"}}}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	return h, ag
}

type claudeFeishuTestRequest struct {
	Handler    *Handler
	SessionKey string
	Text       string
	Choice     string
	UnionID    string
}

func sendClaudeFeishuCommand(req claudeFeishuTestRequest) *platformtest.Replier {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "user", MessageID: firstNonBlank(req.Text, req.Choice), Text: req.Text,
		Metadata: map[string]string{"feishu_session_key": req.SessionKey, "feishu_union_id": req.UnionID},
	}
	if req.Choice != "" {
		msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{"choice": req.Choice}}
	}
	req.Handler.HandleMessage(context.Background(), msg, reply)
	return reply
}
