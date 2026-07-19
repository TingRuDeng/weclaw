package messaging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuClaudeSessionChoicesPaginateWithStableIDs(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	for index := 0; index < 9; index++ {
		ag.catalogSessions = append(ag.catalogSessions, agent.ClaudeSession{
			ID: fmt.Sprintf("session-%02d", index), Cwd: workspace,
			Title: fmt.Sprintf("会话 %02d", index), UpdatedAt: fmt.Sprintf("2026-07-13T%02d:00:00Z", 20-index),
		})
	}

	workspaceChoice := requireFeishuClaudeWorkspaceChoice(t, h, "feishu:user")
	first := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: workspaceChoice})
	if len(first.Choices) != 1 || len(first.Choices[0].Choices) != 9 {
		t.Fatalf("first page=%#v，期望 7 个会话、下一页和返回", first.Choices)
	}
	if got := first.Choices[0].Choices[7].ID; got != "/cc page sessions 2" {
		t.Fatalf("next id=%q，期望会话下一页", got)
	}
	snapshot := first.Choices[0].Choices[7].Metadata["navigation_snapshot"]
	if snapshot == "" {
		t.Fatalf("next=%#v，分页按钮必须绑定服务端快照", first.Choices[0].Choices[7])
	}
	back := first.Choices[0].Choices[8]
	if back.Label != "← 返回上一级" || back.Metadata[platform.ChoiceMetadataButtonType] != platform.ChoiceButtonTypeDefault {
		t.Fatalf("back=%#v，期望独立次级返回按钮", back)
	}

	ag.catalogSessions = append(ag.catalogSessions, agent.ClaudeSession{
		ID: "session-inserted", Cwd: workspace, Title: "插入会话", UpdatedAt: "2026-07-14T23:00:00Z",
	})
	second := sendClaudeFeishuCommand(claudeFeishuTestRequest{
		Handler: h, SessionKey: "feishu:user", Choice: "/cc page sessions 2",
		MessageID: "evt-page-2", Snapshot: snapshot,
	})
	if len(second.Choices) != 1 || !strings.Contains(second.Choices[0].Prompt, "第 2/2 页") {
		t.Fatalf("second page=%#v，期望第二页卡片", second.Choices)
	}
	choices := second.Choices[0].Choices
	if len(choices) != 4 || choices[0].ID != "/cc switch session-07" || choices[1].ID != "/cc switch session-08" || choices[2].ID != "/cc page sessions 1" || choices[3].ID != "/cc cd .." {
		t.Fatalf("second choices=%#v，会话分页必须保留稳定 sessionId", choices)
	}
}

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
	for _, choice := range reply.Choices[0].Choices {
		if !isTestFeishuWorkspaceChoice(choice.ID, "/cc") || strings.Contains(choice.ID, allowedRoot) {
			t.Fatalf("workspace choice=%#v，必须使用不泄露路径的 opaque token", choice)
		}
	}
}

func TestFeishuClaudeWorkspaceChoiceKeepsOriginalTargetAfterCatalogReorder(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	root := t.TempDir()
	beta := filepath.Join(root, "beta")
	alpha := filepath.Join(root, "alpha")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	h.SetAllowedWorkspaceRoots([]string{root})
	ag.catalogSessions = []agent.ClaudeSession{
		{ID: "session-beta-1", Cwd: beta, Title: "Beta 1"},
		{ID: "session-beta-2", Cwd: beta, Title: "Beta 2"},
	}
	listed := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc ls"})
	if len(listed.Choices) != 1 || len(listed.Choices[0].Choices) != 1 {
		t.Fatalf("listed choices=%#v", listed.Choices)
	}
	staleChoice := listed.Choices[0].Choices[0].ID

	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	ag.catalogSessions = append(ag.catalogSessions, agent.ClaudeSession{ID: "session-alpha", Cwd: alpha, Title: "Alpha"})
	clicked := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: staleChoice})
	binding := h.ensureClaudeSessions().binding(claudeBindingKey("feishu:user", "claude"))
	if binding.WorkspaceRoot != normalizeClaudeWorkspaceRoot(beta) {
		t.Fatalf("binding=%+v, want original beta %q; choice=%q texts=%#v", binding, beta, staleChoice, clicked.Texts)
	}
}

func TestFeishuClaudeNewAppearsBeforeACPCatalogPersists(t *testing.T) {
	h, ag := newClaudeFeishuCardHandler(t)
	workspace := t.TempDir()
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	ag.resetSessionID = "session-new"

	created := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc new"})
	if len(created.Texts) != 1 || !strings.Contains(created.Texts[0], "已创建并绑定") {
		t.Fatalf("created texts=%#v choices=%#v", created.Texts, created.Choices)
	}

	listed := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Text: "/cc ls"})
	if len(listed.Texts) != 0 || len(listed.Choices) != 1 || len(listed.Choices[0].Choices) != 1 {
		t.Fatalf("listed texts=%#v choices=%#v，空会话落入 ACP 目录前也应显示当前工作空间", listed.Texts, listed.Choices)
	}
	if choice := listed.Choices[0].Choices[0]; !isTestFeishuWorkspaceChoice(choice.ID, "/cc") || !strings.Contains(choice.Label, "当前新会话") {
		t.Fatalf("choice=%#v，期望标记当前暂态会话", choice)
	}

	opened := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: listed.Choices[0].Choices[0].ID})
	if len(opened.Choices) != 0 || len(opened.Texts) != 1 || !strings.Contains(opened.Texts[0], "发送第一条消息") {
		t.Fatalf("opened texts=%#v choices=%#v，暂态会话应提示先发送首条消息", opened.Texts, opened.Choices)
	}
	key := claudeBindingKey("feishu:user", "claude")
	if binding := h.ensureClaudeSessions().binding(key); binding.SessionID != "session-new" || binding.Status != claudeBindingReady {
		t.Fatalf("binding=%+v，浏览暂态工作空间不得释放当前会话", binding)
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
	workspaceChoice := requireFeishuClaudeWorkspaceChoice(t, h, "feishu:user")
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: workspaceChoice})

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
	ag.runtimeSessions = map[string]string{conversationID: "session-a"}

	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: "feishu:user", Choice: "/cc switch session-b"})
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "已切换 Claude 会话") {
		t.Fatalf("texts=%#v choices=%#v", reply.Texts, reply.Choices)
	}
	if got := store.binding(key); got.SessionID != "session-b" || got.Status != claudeBindingReady {
		t.Fatalf("binding=%+v", got)
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

func requireFeishuClaudeWorkspaceChoice(t *testing.T, h *Handler, sessionKey string) string {
	t.Helper()
	reply := sendClaudeFeishuCommand(claudeFeishuTestRequest{Handler: h, SessionKey: sessionKey, Text: "/cc ls"})
	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) == 0 {
		t.Fatalf("workspace choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
	choice := reply.Choices[0].Choices[0]
	if !isTestFeishuWorkspaceChoice(choice.ID, "/cc") {
		t.Fatalf("workspace choice=%#v, want opaque token", choice)
	}
	return choice.ID
}

type claudeFeishuTestRequest struct {
	Handler    *Handler
	SessionKey string
	Text       string
	Choice     string
	UnionID    string
	MessageID  string
	Snapshot   string
}

func sendClaudeFeishuCommand(req claudeFeishuTestRequest) *platformtest.Replier {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "user", MessageID: firstNonBlank(req.MessageID, req.Text, req.Choice), Text: req.Text,
		Metadata: map[string]string{"feishu_session_key": req.SessionKey, "feishu_union_id": req.UnionID},
	}
	if req.Choice != "" {
		msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": req.Choice, "navigation_snapshot": req.Snapshot,
		}}
	}
	req.Handler.HandleMessage(context.Background(), msg, reply)
	return reply
}
