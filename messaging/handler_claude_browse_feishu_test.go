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

func TestFeishuClaudeCcLsSendsAllowedWorkspaceChoices(t *testing.T) {
	h, _, claudeDir := newClaudeFeishuCardHandler(t)
	allowedRoot := t.TempDir()
	workspaceA := filepath.Join(allowedRoot, "alpha")
	workspaceB := filepath.Join(allowedRoot, "beta")
	blockedWorkspace := filepath.Join(t.TempDir(), "blocked")
	h.SetAllowedWorkspaceRoots([]string{allowedRoot})
	writeLocalClaudeSession(t, claudeDir, "session-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-b", workspaceB, "Beta 会话", "2026-04-29T08:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-blocked", blockedWorkspace, "越权会话", "2026-04-29T10:00:00Z")
	sessionKey := "feishu:tenant:dm:chat:user"
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Text: "/cc ls"})

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v，期望一张工作空间卡片", reply.Choices)
	}
	choices := reply.Choices[0].Choices
	if len(choices) != 2 || choices[0].ID != "/cc cd 0" || choices[0].Label != "alpha" {
		t.Fatalf("workspace choices=%#v，期望两个已授权工作空间", choices)
	}
	for _, choice := range choices {
		if choice.Metadata["feishu_session_key"] != sessionKey {
			t.Fatalf("choice=%#v，期望保留飞书会话路由", choice)
		}
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts=%#v，卡片发送成功时不应重复发送文本", reply.Texts)
	}
}

func TestFeishuClaudeWorkspaceChoiceSendsStableSessionChoices(t *testing.T) {
	h, ag, claudeDir := newClaudeFeishuCardHandler(t)
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-new", workspace, "较新会话", "2026-04-29T09:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-old", workspace, "较早会话", "2026-04-29T08:00:00Z")
	sessionKey := "feishu:tenant:dm:chat:user"
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Choice: "/cc cd 0"})

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v，期望一张会话卡片", reply.Choices)
	}
	choices := reply.Choices[0].Choices
	if len(choices) != 3 || choices[0].ID != "/cc switch session-new" || choices[0].Label != "较新会话" {
		t.Fatalf("session choices=%#v，期望使用稳定 sessionId", choices)
	}
	if choices[2].ID != "/cc cd .." || choices[2].Label != "返回工作空间列表" {
		t.Fatalf("back choice=%#v，期望返回工作空间按钮", choices[2])
	}
	bindingKey := claudeBindingKey(sessionKey, "claude")
	active, ok := h.ensureClaudeSessions().getActiveWorkspace(bindingKey)
	if !ok || active != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("active workspace=(%q,%t)，期望 %q", active, ok, workspace)
	}
	conversationID := buildClaudeConversationID(sessionKey, "claude", workspace)
	if ag.conversationCwds[conversationID] != normalizeClaudeWorkspaceRoot(workspace) {
		t.Fatalf("conversation cwd=%q，期望 %q", ag.conversationCwds[conversationID], workspace)
	}
}

func TestFeishuClaudeSessionChoiceSwitchesStableSessionAndShowsModel(t *testing.T) {
	h, ag, claudeDir := newClaudeFeishuCardHandler(t)
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-target", workspace, "目标会话", "2026-04-29T09:00:00Z")
	appendClaudeSessionStatus(t, claudeSessionStatusFixture{ClaudeDir: claudeDir, Workspace: workspace, SessionID: "session-target"})
	sessionKey := "feishu:tenant:dm:chat:user"
	card := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Choice: "/cc cd 0"})
	choice := card.Choices[0].Choices[0].ID
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Choice: choice})

	if ag.useSessionID != "session-target" {
		t.Fatalf("switched session=%q，期望 session-target", ag.useSessionID)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "模型: claude-opus-4-1") {
		t.Fatalf("texts=%#v，期望显示会话模型", reply.Texts)
	}
	if !strings.Contains(reply.Texts[0], "推理强度: high") {
		t.Fatalf("texts=%#v，期望显示会话推理强度", reply.Texts)
	}
}

func TestFeishuClaudeCcLsAllowsConfiguredAdminOutsideRoots(t *testing.T) {
	h, _, claudeDir := newClaudeFeishuCardHandler(t)
	workspaceA := filepath.Join(t.TempDir(), "alpha")
	workspaceB := filepath.Join(t.TempDir(), "beta")
	h.SetAdminUsers([]string{"on_admin"})
	writeLocalClaudeSession(t, claudeDir, "session-a", workspaceA, "会话 A", "2026-04-29T09:00:00Z")
	writeLocalClaudeSession(t, claudeDir, "session-b", workspaceB, "会话 B", "2026-04-29T08:00:00Z")
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{
		Handler: h, SessionKey: "feishu:admin", Text: "/cc ls", UnionID: "on_admin",
	})

	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v，管理员应看到白名单外工作空间", reply.Choices)
	}
}

func TestFeishuClaudeInvalidWorkspaceUsesClaudeHelpCommand(t *testing.T) {
	h, _, claudeDir := newClaudeFeishuCardHandler(t)
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc cd missing"})

	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v，无效工作空间只应返回错误文本", reply.Choices)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "/cc ls") || strings.Contains(reply.Texts[0], "/cx ls") {
		t.Fatalf("texts=%#v，期望使用 Claude 列表命令引导", reply.Texts)
	}
}

func TestFeishuClaudeEmptyWorkspacePromptsExplicitNew(t *testing.T) {
	h, _, _ := newClaudeFeishuCardHandler(t)
	workspace := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("创建空工作空间失败：%v", err)
	}
	h.SetAllowedWorkspaceRoots([]string{workspace})
	sessionKey := "feishu:tenant:dm:chat:user"
	bindingKey := claudeBindingKey(sessionKey, "claude")
	h.ensureClaudeSessions().setPendingNew(bindingKey, workspace)
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Choice: "/cc cd 0"})

	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v，空工作空间不应伪造会话按钮", reply.Choices)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "/cc new") {
		t.Fatalf("texts=%#v，期望提示显式创建会话", reply.Texts)
	}
}

func TestFeishuClaudeCcLsDuringActiveTaskDoesNotSendCard(t *testing.T) {
	h, ag, claudeDir := newClaudeFeishuCardHandler(t)
	workspace := filepath.Join(t.TempDir(), "project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	sessionKey := "feishu:tenant:dm:chat:user"
	h.ensureClaudeSessions().setActiveWorkspace(claudeBindingKey(sessionKey, "claude"), workspace)
	key := h.agentExecutionKeyForRoute("user", sessionKey, "claude", ag)
	task, _, started := h.beginActiveTask(context.Background(), key, activeTaskMeta{owner: "user", agentName: "claude", message: "执行中"})
	if !started {
		t.Fatal("Claude active task should start")
	}
	defer h.finishActiveTask(key, task)
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Text: "/cc ls"})

	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v，运行中不应发送导航卡片", reply.Choices)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "当前任务正在执行") {
		t.Fatalf("texts=%#v，期望运行中提示", reply.Texts)
	}
}

// newClaudeFeishuCardHandler 构造只启用 Claude 的飞书卡片测试环境。
func newClaudeFeishuCardHandler(t *testing.T) (*Handler, *fakeClaudeSessionAgent, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	ag := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"}}}
	h.defaultName = "claude"
	h.agents["claude"] = ag
	h.SetClaudeLocalSessionDir(claudeDir)
	return h, ag, claudeDir
}

type claudeSessionStatusFixture struct {
	ClaudeDir string
	Workspace string
	SessionID string
}

// appendClaudeSessionStatus 写入模型状态，验证切换反馈读取真实会话记录。
func appendClaudeSessionStatus(t *testing.T, fixture claudeSessionStatusFixture) {
	t.Helper()
	path := filepath.Join(fixture.ClaudeDir, "projects", encodeClaudeProjectPath(fixture.Workspace), fixture.SessionID+".jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("打开 Claude 会话记录失败：%v", err)
	}
	defer file.Close()
	record := `{"type":"assistant","effortLevel":"high","message":{"model":"claude-opus-4-1"}}` + "\n"
	if _, err := file.WriteString(record); err != nil {
		t.Fatalf("写入 Claude 会话状态失败：%v", err)
	}
}

type claudeFeishuTestRequest struct {
	Handler    *Handler
	SessionKey string
	Text       string
	Choice     string
	UnionID    string
	MessageID  string
}

// sendClaudeFeishuCommand 使用真实平台路由发送文本命令或卡片回放命令。
func sendClaudeFeishuCommand(req claudeFeishuTestRequest) *platformtest.Replier {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "user", MessageID: firstNonBlank(req.MessageID, req.Text, req.Choice), Text: req.Text,
		Metadata: map[string]string{"feishu_session_key": req.SessionKey, "feishu_union_id": req.UnionID},
	}
	if req.Choice != "" {
		msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{"choice": req.Choice}}
	}
	req.Handler.HandleMessage(context.Background(), msg, reply)
	return reply
}
