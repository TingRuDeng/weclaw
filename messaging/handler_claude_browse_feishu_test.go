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

func TestFeishuClaudeNewAppearsBeforeACPCatalogPersists(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag.resetSessionID = "session-new"

	created := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc new"})
	if len(created.Texts) != 1 || !strings.Contains(created.Texts[0], "已创建并接管") {
		t.Fatalf("created texts=%#v choices=%#v", created.Texts, created.Choices)
	}

	listed := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc ls"})
	if len(listed.Texts) != 0 || len(listed.Choices) != 1 || len(listed.Choices[0].Choices) != 1 {
		t.Fatalf("listed texts=%#v choices=%#v，空会话落入 ACP 目录前也应显示当前工作空间", listed.Texts, listed.Choices)
	}
	if choice := listed.Choices[0].Choices[0]; choice.ID != "/cc cd 0" || !strings.Contains(choice.Label, "当前新会话") {
		t.Fatalf("choice=%#v，期望标记当前暂态会话", choice)
	}

	opened := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: "/cc cd 0"})
	if len(opened.Choices) != 0 || len(opened.Texts) != 1 || !strings.Contains(opened.Texts[0], "发送第一条消息") {
		t.Fatalf("opened texts=%#v choices=%#v，暂态会话应提示先发送首条消息", opened.Texts, opened.Choices)
	}
	key := claudeBindingKey("feishu:user", "claude")
	if binding := h.ensureClaudeSessions().binding(key); binding.SessionID != "session-new" || binding.Status != claudeBindingReady {
		t.Fatalf("binding=%+v，浏览暂态工作空间不得释放当前会话", binding)
	}
	if intent := h.ensureClaudeSessions().controlIntent("session-new"); intent.Owner != claudeOwnerRemote || intent.BindingKey != key {
		t.Fatalf("intent=%+v，浏览暂态工作空间不得释放 owner", intent)
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
	if len(h.ensureClaudeSessions().controls) != 0 {
		t.Fatalf("controls=%+v，工作空间卡片本身不应取得 session owner", h.ensureClaudeSessions().controls)
	}
}

func TestFeishuClaudeSessionChoiceSwitchesRemoteOwner(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-b", Cwd: workspace, Title: "目标会话"},
	}
	key := claudeBindingKey("feishu:user", "claude")
	conversationID := buildClaudeConversationID("feishu:user", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	ag.runtimeSessions = map[string]string{conversationID: "session-a"}

	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: "/cc switch session-b"})
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "已切换并接管") {
		t.Fatalf("texts=%#v choices=%#v", reply.Texts, reply.Choices)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("old intent=%+v", got)
	}
	if got := store.controlIntent("session-b"); got.Owner != claudeOwnerRemote || got.BindingKey != key {
		t.Fatalf("new intent=%+v", got)
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
