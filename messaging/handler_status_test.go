package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

func TestCodexCleanRemovesMissingStoredWorkspaces(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	missingWorkspace := filepath.Join(t.TempDir(), "missing")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
		},
		threadID: "thread-current",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetCodexLocalSessionDir(t.TempDir())
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, missingWorkspace, "thread-missing")
	h.codexSessions.setActiveWorkspace(bindingKey, missingWorkspace)
	h.setCodexBrowseWorkspace(bindingKey, missingWorkspace)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(109, "/cx clean"))

	texts := calls.texts()
	if !containsText(texts, "已清理 Codex 工作空间：1 个") {
		t.Fatalf("clean reply should include removed count, messages=%#v", texts)
	}
	if !containsText(texts, filepath.Base(missingWorkspace)) {
		t.Fatalf("clean reply should include removed workspace name, messages=%#v", texts)
	}
	if thread, _ := h.codexSessions.getThread(bindingKey, missingWorkspace); thread != "" {
		t.Fatalf("missing workspace thread=%q, want empty after clean", thread)
	}
	if browse, ok := h.codexBrowseWorkspace(bindingKey); ok || browse != "" {
		t.Fatalf("browse workspace=(%q,%v), want cleared", browse, ok)
	}
}

func TestStatusCommandUsesGlobalStatusAndInfoDoesNotCallAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{
		reply: "默认回复",
		info:  agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-test", Command: "codex"},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	h.SetProgressConfig(cfg)

	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(130, "/status"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(131, "/info"))

	texts := calls.texts()
	if !containsText(texts, "agent: codex") || !containsText(texts, "(acp)") {
		t.Fatalf("status reply mismatch, messages=%#v", texts)
	}
	if !containsText(texts, "请使用 /status") {
		t.Fatalf("info migration reply mismatch, messages=%#v", texts)
	}
	if ag.chatCallCount() != 0 {
		t.Fatalf("/info should not call default agent, calls=%d", ag.chatCallCount())
	}
}

func TestStatusCommandShowsRuntimeMetrics(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}}
	h.SetRateLimitPerMinute(30)
	h.setYoloMode("user-1", true)
	h.agentInvocations.Store(5)
	h.agentErrors.Store(2)
	_, _, _ = h.beginActiveTask(context.Background(), "k1", activeTaskMeta{owner: "user-1", agentName: "codex", message: "x"})

	text := h.buildStatus("user-1")
	for _, want := range []string{"running tasks: 1 (you: 1)", "agent calls: 5, errors: 2", "mode: yolo", "rate limit: 30/min", "uptime:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status missing %q, got %q", want, text)
		}
	}
}

func TestStatusCommandShowsDefaultModelWhenModelEmpty(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}

	text := h.buildStatus("user-1")

	if !strings.Contains(text, "model: (Agent 默认)") {
		t.Fatalf("status should show default model, got %q", text)
	}
}

func TestCommandRepliesUseBlankLinesForWeChat(t *testing.T) {
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Model: "gpt-test", Command: "codex"},
	}

	tests := map[string]string{
		"status":      h.buildStatus("user-1"),
		"cwd":         h.handleCwd("/cwd"),
		"progress":    h.handleProgressCommand("/progress"),
		"progressErr": h.handleProgressCommand("/progress unknown"),
		"codexHelp":   buildCodexSessionHelpText(),
	}

	for name, text := range tests {
		if strings.Contains(text, "\n") && !strings.Contains(text, "\n\n") {
			t.Fatalf("%s reply should use blank lines for WeChat rendering, got %q", name, text)
		}
	}
}

func TestCodexWorkspaceRepliesUseBlankLinesForWeChat(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	bindingKey := codexBindingKey("user-1", "codex")
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setPendingNew(bindingKey, workspaceB)

	where := h.renderCodexWhoami(bindingKey, workspaceA)
	if !strings.Contains(where, "workspace: "+workspaceA+"\n\nthread: thread-a") {
		t.Fatalf("where reply should separate fields with blank lines, got %q", where)
	}

	list := h.renderCodexList(bindingKey)
	for _, want := range []string{
		"Codex 工作空间:\n\n0. ",
		filepath.Base(workspaceA),
		filepath.Base(workspaceB),
	} {
		if !strings.Contains(list, want) {
			t.Fatalf("workspace reply missing %q, got %q", want, list)
		}
	}
	if strings.Contains(list, "thread-a") || strings.Contains(list, workspaceA) {
		t.Fatalf("workspace reply should hide thread ids and full paths, got %q", list)
	}
}
