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

func TestFeishuCodexWorkspaceChoicesUseStablePagination(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	for index := 0; index < 9; index++ {
		workspace := filepath.Join(root, fmt.Sprintf("workspace-%02d", index))
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		writeLocalCodexSession(
			t, codexDir, fmt.Sprintf("thread-%02d", index), workspace,
			fmt.Sprintf("会话 %02d", index), fmt.Sprintf("2026-04-%02dT09:00:00Z", 29-index),
		)
	}
	h.SetAllowedWorkspaceRoots([]string{root})
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"

	first := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", Text: "/cx ls",
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, first)
	if len(first.Choices) != 1 || len(first.Choices[0].Choices) != 8 {
		t.Fatalf("first page=%#v，期望 7 个工作空间和下一页", first.Choices)
	}
	next := first.Choices[0].Choices[7]
	if next.ID != "/cx page workspaces 2" || next.Metadata[platform.ChoiceMetadataSection] != platform.ChoiceSectionNavigation {
		t.Fatalf("next=%#v，期望次级样式的下一页动作", next)
	}
	snapshot := next.Metadata["navigation_snapshot"]
	if snapshot == "" {
		t.Fatalf("next=%#v，分页按钮必须绑定服务端快照", next)
	}
	inserted := filepath.Join(root, "workspace--inserted")
	if err := os.MkdirAll(inserted, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLocalCodexSession(t, codexDir, "thread-inserted", inserted, "插入会话", "2026-04-30T09:00:00Z")

	second := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: "evt-page-2",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": "/cx page workspaces 2", "navigation_snapshot": snapshot,
		}},
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, second)
	if len(second.Choices) != 1 || !strings.Contains(second.Choices[0].Prompt, "第 2/2 页") {
		t.Fatalf("second page=%#v，期望第二页卡片", second.Choices)
	}
	choices := second.Choices[0].Choices
	if len(choices) != 3 || choices[0].Label != "workspace-07" || choices[1].Label != "workspace-08" ||
		!isTestFeishuWorkspaceChoice(choices[0].ID, "/cx") || !isTestFeishuWorkspaceChoice(choices[1].ID, "/cx") ||
		choices[2].ID != "/cx page workspaces 1" {
		t.Fatalf("second choices=%#v，分页必须使用首次加载时的稳定快照", choices)
	}

	firstAgain := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: "evt-page-1",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": "/cx page workspaces 1", "navigation_snapshot": snapshot,
		}},
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, firstAgain)
	if len(firstAgain.Choices) != 1 || !strings.Contains(firstAgain.Choices[0].Prompt, "第 1/2 页") {
		t.Fatalf("first again=%#v，期望返回第一页", firstAgain.Choices)
	}

	secondAgain := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: "evt-page-2-again",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{
			"choice": "/cx page workspaces 2", "navigation_snapshot": snapshot,
		}},
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, secondAgain)
	if len(secondAgain.Choices) != 1 || !strings.Contains(secondAgain.Choices[0].Prompt, "第 2/2 页") {
		t.Fatalf("second again=%#v，往返后必须还能再次进入第二页", secondAgain.Choices)
	}
}

func TestFeishuCodexCxLsSendsWorkspaceChoices(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-a2", workspaceA, "Alpha 会话 2", "2026-04-29T08:30:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	sessionKey := "feishu:tenant_1:group:oc_1:om_root"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-ls",
		Text:      "/cx ls",
		Metadata:  map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v, want workspace choice card", reply.Choices)
	}
	choices := reply.Choices[0].Choices
	if len(choices) != 2 {
		t.Fatalf("workspace choices=%#v, want two workspaces", choices)
	}
	if !isTestFeishuWorkspaceChoice(choices[0].ID, "/cx") || choices[0].Label != "alpha" {
		t.Fatalf("first workspace choice=%#v, want opaque alpha token", choices[0])
	}
	if strings.Contains(choices[0].ID, workspaceA) {
		t.Fatalf("workspace choice leaked absolute path: %q", choices[0].ID)
	}
	for _, choice := range choices {
		if choice.Metadata["feishu_session_key"] != sessionKey {
			t.Fatalf("choice=%#v, want feishu session metadata %q", choice, sessionKey)
		}
	}
	if len(reply.Texts) != 0 {
		t.Fatalf("texts=%#v, want no text reply when card choices are available", reply.Texts)
	}
}

func TestFeishuCodexWorkspaceChoiceKeepsOriginalTargetAfterCatalogReorder(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir, root := t.TempDir(), t.TempDir()
	beta := filepath.Join(root, "beta")
	alpha := filepath.Join(root, "alpha")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-beta-1", beta, "Beta 1", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-beta-2", beta, "Beta 2", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	sessionKey := "feishu:tenant_1:dm:oc_1:ou_user"
	listed := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", Text: "/cx ls",
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, listed)
	if len(listed.Choices) != 1 || len(listed.Choices[0].Choices) != 1 {
		t.Fatalf("listed choices=%#v", listed.Choices)
	}
	staleChoice := listed.Choices[0].Choices[0].ID

	writeLocalCodexSession(t, codexDir, "thread-alpha", alpha, "Alpha", "2026-04-29T10:00:00Z")
	clicked := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": staleChoice}},
		Metadata:   map[string]string{feishuSessionMetadataKey: sessionKey},
	}, clicked)

	bindingKey := codexBindingKey(sessionKey, "codex")
	workspace, ok := h.codexBrowseWorkspace(bindingKey)
	if !ok || workspace != normalizeCodexWorkspaceRoot(beta) {
		t.Fatalf("workspace=%q ok=%t, want original beta %q; choice=%q texts=%#v", workspace, ok, beta, staleChoice, clicked.Texts)
	}
}

// TestFeishuCodexWorkspaceNameWithErrorWordStillSendsCard 验证业务文本不会被误判为命令错误。
func TestFeishuCodexWorkspaceNameWithErrorWordStillSendsCard(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "失败案例")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		MessageID: "feishu-cx-error-word", Text: "/cx ls",
	}, reply)

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v texts=%#v, want workspace choice card", reply.Choices, reply.Texts)
	}
}

func TestFeishuCodexWorkspaceChoiceKeepsAliasAdminAccess(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	h.SetAdminUsers([]string{"on_admin"})
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	sessionKey := "feishu:tenant_1:dm:oc_1:ou_open"

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_open", UserAliases: []string{"on_admin"},
		MessageID: "feishu-alias-admin-list", Text: "/cx ls",
		Metadata: map[string]string{"feishu_session_key": sessionKey},
	}, reply)
	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("workspace choices=%#v, want two admin-visible workspaces", reply.Choices)
	}

	workspaceChoice := reply.Choices[0].Choices[1].ID
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_open", UserAliases: []string{"on_admin"},
		MessageID:  "feishu-alias-admin-choice",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": workspaceChoice}},
		Metadata:   map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	bindingKey := codexBindingKey(sessionKey, "codex")
	activeWorkspace, ok := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	if !ok || activeWorkspace != normalizeCodexWorkspaceRoot(workspaceB) {
		t.Fatalf("active workspace=%q ok=%t, want alias admin workspace %q; texts=%#v", activeWorkspace, ok, workspaceB, reply.Texts)
	}
}

func TestFeishuCodexCxLsDuringActiveTaskDoesNotSendNavigationCard(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	sessionKey := "feishu:tenant_1:dm:oc_1:ou_user"
	route := h.codexConversationRouteForSession("ou_user", sessionKey, "codex", ag)
	task, _, started := h.beginActiveTask(context.Background(), route.conversationID, activeTaskMeta{
		owner:     "ou_user",
		agentName: "codex",
		message:   "正在执行的任务",
	})
	if !started {
		t.Fatal("active task should start")
	}
	defer h.finishActiveTask(route.conversationID, task)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-ls-running",
		Text:      "/cx ls",
		Metadata:  map[string]string{"feishu_session_key": sessionKey},
	}, reply)

	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v, want no navigation card while task is running", reply.Choices)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "当前任务正在执行，请在完成后再发送 /cx ls。" {
		t.Fatalf("texts=%#v, want running task notice", reply.Texts)
	}
}

func TestFeishuCodexWorkspaceChoiceSendsSessionChoices(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspace, "会话 B", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	workspaceChoice := requireFeishuCodexWorkspaceChoice(t, h, "ou_user", "", "weclaw", nil)

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-workspace",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": workspaceChoice},
		},
	}, reply)

	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspace) {
		t.Fatalf("codex cwd=%q, want %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspace))
	}
	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v, want session choice card", reply.Choices)
	}
	if !strings.Contains(reply.Choices[0].Prompt, "weclaw 会话") {
		t.Fatalf("prompt=%q, want workspace session prompt", reply.Choices[0].Prompt)
	}
	choices := reply.Choices[0].Choices
	if len(choices) != 3 || choices[0].ID != "/cx switch thread-a" || choices[0].Label != "会话 A" {
		t.Fatalf("session choices=%#v, want switch choices", choices)
	}
	if choices[2].ID != "/cx cd .." || choices[2].Label != "← 返回上一级" {
		t.Fatalf("last session choice=%#v, want back to workspace list", choices[2])
	}
	bindingKey := codexBindingKey("ou_user", "codex")
	active, _ := h.codexSessions.getActiveWorkspace(bindingKey)
	intentA := h.codexSessions.controlIntent("thread-a")
	intentB := h.codexSessions.controlIntent("thread-b")
	if active != normalizeCodexWorkspaceRoot(workspace) ||
		intentA.Owner != codexControlUnclaimed || intentB.Owner != codexControlUnclaimed {
		t.Fatalf("active=%q intents=(%#v,%#v)", active, intentA, intentB)
	}
}

func TestFeishuCodexWorkspaceChoiceAutoAcquiresSingleSessionWithoutSecondCard(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	workspaceChoice := requireFeishuCodexWorkspaceChoice(t, h, "ou_user", "", "weclaw", nil)

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-workspace-single",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": workspaceChoice},
		},
	}, reply)

	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v, want no second choice card", reply.Choices)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "已进入工作空间并接管唯一会话") {
		t.Fatalf("texts=%#v, want one acquire success reply", reply.Texts)
	}
	bindingKey := codexBindingKey("ou_user", "codex")
	intent := h.codexSessions.controlIntent("thread-a")
	if ag.useThreadID != "" || intent.Owner != codexControlRemote || intent.RouteBindingKey != bindingKey {
		t.Fatalf("use=%q intent=%#v", ag.useThreadID, intent)
	}
}

func TestFeishuCodexSessionChoicesCanReturnToWorkspaceList(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-a2", workspaceA, "Alpha 会话 2", "2026-04-29T08:30:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	workspaceChoice := requireFeishuCodexWorkspaceChoice(t, h, "ou_user", "", "alpha", nil)

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-workspace",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": workspaceChoice},
		},
	}, reply)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-back",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "/cx cd .."},
		},
	}, reply)

	if len(reply.Choices) != 2 {
		t.Fatalf("choices=%#v, want session card then workspace card", reply.Choices)
	}
	workspaceChoices := reply.Choices[1].Choices
	if len(workspaceChoices) != 2 || !isTestFeishuWorkspaceChoice(workspaceChoices[0].ID, "/cx") || workspaceChoices[0].Label != "alpha" {
		t.Fatalf("workspace choices=%#v, want workspace list after back", workspaceChoices)
	}
}

func TestFeishuCodexStaleSessionChoiceSwitchesOriginalThread(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
	appendLocalCodexTurnContext(t, codexDir, "thread-a", "gpt-5.5", "high")
	writeLocalCodexSession(t, codexDir, "thread-a2", workspaceA, "Alpha 会话 2", "2026-04-29T08:30:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspaceB, "Beta 会话", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	h.defaultName = "codex"
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	alphaChoice := requireFeishuCodexWorkspaceChoice(t, h, "ou_user", "", "alpha", nil)

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-alpha",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": alphaChoice},
		},
	}, reply)
	staleAlphaChoice := reply.Choices[0].Choices[0].ID
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": "/cx cd .."}},
	}, platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true}))
	betaChoice := requireFeishuCodexWorkspaceChoice(t, h, "ou_user", "", "beta", nil)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-beta",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": betaChoice},
		},
	}, reply)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-stale-alpha",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": staleAlphaChoice},
		},
	}, reply)

	intentA := h.codexSessions.controlIntent("thread-a")
	intentB := h.codexSessions.controlIntent("thread-b")
	if ag.useThreadID != "" || intentA.Owner != codexControlRemote || intentB.Owner != codexControlDesktop {
		t.Fatalf("use=%q intents=(%#v,%#v)", ag.useThreadID, intentA, intentB)
	}
	if ag.lastWorkingDir() != normalizeCodexWorkspaceRoot(workspaceA) {
		t.Fatalf("stale card cwd=%q, want original workspace %q", ag.lastWorkingDir(), normalizeCodexWorkspaceRoot(workspaceA))
	}
	if len(reply.Texts) == 0 || !strings.Contains(reply.Texts[len(reply.Texts)-1], "模型: gpt-5.5") {
		t.Fatalf("card switch should show session model status, texts=%#v", reply.Texts)
	}
}

func requireFeishuCodexWorkspaceChoice(t *testing.T, h *Handler, userID string, sessionKey string, label string, aliases []string) string {
	t.Helper()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	metadata := map[string]string{}
	if sessionKey != "" {
		metadata[feishuSessionMetadataKey] = sessionKey
	}
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: userID, UserAliases: aliases,
		Text: "/cx ls", Metadata: metadata,
	}, reply)
	if len(reply.Choices) != 1 {
		t.Fatalf("workspace choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
	for _, choice := range reply.Choices[0].Choices {
		if choice.Label == label {
			if !isTestFeishuWorkspaceChoice(choice.ID, "/cx") {
				t.Fatalf("workspace choice=%#v, want opaque token", choice)
			}
			return choice.ID
		}
	}
	t.Fatalf("workspace %q not found in %#v", label, reply.Choices[0].Choices)
	return ""
}

func isTestFeishuWorkspaceChoice(command string, prefix string) bool {
	fields := strings.Fields(command)
	return len(fields) == 3 && fields[0] == prefix && fields[1] == "cd" && isFeishuWorkspaceChoiceToken(fields[2])
}

func TestFeishuCodexInvalidWorkspaceReturnsTextError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-invalid-workspace",
		Text:      "/cx cd missing",
	}, reply)

	if len(reply.Choices) != 0 {
		t.Fatalf("choices=%#v, want text error only", reply.Choices)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "工作空间不存在") {
		t.Fatalf("texts=%#v, want missing workspace error", reply.Texts)
	}
}
