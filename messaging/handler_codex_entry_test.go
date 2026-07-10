package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexAttachOpensVisibleCompanion(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeVisibleCodexAgent{
		fakeCodexThreadAgent: fakeCodexThreadAgent{
			fakeAgent: fakeAgent{
				info: agent.AgentInfo{Name: "codex", Type: "companion", Command: "codex"},
			},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(123, "/cx attach"))

	if ag.openCalls != 1 {
		t.Fatalf("OpenVisibleCompanion calls=%d, want 1", ag.openCalls)
	}
	if !containsText(calls.texts(), "已打开 Codex 本地可见端") {
		t.Fatalf("attach reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexDetachClosesVisibleCompanionOnly(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeVisibleCodexAgent{
		fakeCodexThreadAgent: fakeCodexThreadAgent{
			fakeAgent: fakeAgent{
				info: agent.AgentInfo{Name: "codex", Type: "companion", Command: "codex"},
			},
		},
		detachOK: true,
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(124, "/cx detach"))

	if ag.detachCalls != 1 {
		t.Fatalf("DetachVisibleCompanion calls=%d, want 1", ag.detachCalls)
	}
	if !containsText(calls.texts(), "已断开 Codex 本地可见端") {
		t.Fatalf("detach reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAttachRequiresVisibleCompanion(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "cli", Command: "codex"},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx attach"))

	if !containsText(calls.texts(), "当前 Codex Agent 不支持 attach") {
		t.Fatalf("attach unsupported reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAttachResumesRemoteFirstThreadInTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	var opened []recordedCodexCLIResume
	h.SetCodexCLIResumeOpener(func(_ context.Context, command string, workspace string, threadID string) error {
		opened = append(opened, recordedCodexCLIResume{command: command, workspace: workspace, threadID: threadID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx attach"))

	if len(opened) != 1 || opened[0].command != "codex-bin" || opened[0].workspace != workspace || opened[0].threadID != "thread-1" {
		t.Fatalf("opened=%#v, want codex-bin/%s/thread-1", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex 本地可见端") || !containsText(calls.texts(), "thread-1") {
		t.Fatalf("attach reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexCliCommandResumesRemoteFirstThreadInTerminal(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	var opened []recordedCodexCLIResume
	h.SetCodexCLIResumeOpener(func(_ context.Context, command string, workspace string, threadID string) error {
		opened = append(opened, recordedCodexCLIResume{command: command, workspace: workspace, threadID: threadID})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(129, "/cx cli"))

	if len(opened) != 1 || opened[0].command != "codex-bin" || opened[0].workspace != workspace || opened[0].threadID != "thread-1" {
		t.Fatalf("opened=%#v, want codex-bin/%s/thread-1", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex CLI") || !containsText(calls.texts(), "thread-1") {
		t.Fatalf("cli reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAttachRequiresRecordedThreadForRemoteFirstAgent(t *testing.T) {
	h := NewHandler(nil, nil)
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(128, "/cx attach"))

	if !containsText(calls.texts(), "当前还没有可接手的 Codex thread") {
		t.Fatalf("attach without thread reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAppCommandOpensCurrentWorkspaceWithThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	var opened []recordedCodexAppOpen
	h.SetCodexAppOpener(func(_ context.Context, command string, workspace string) error {
		opened = append(opened, recordedCodexAppOpen{command: command, workspace: workspace})
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(126, "/cx app"))

	if len(opened) != 1 || opened[0].command != "codex-bin" || opened[0].workspace != workspace {
		t.Fatalf("opened=%#v, want codex-bin/%s", opened, workspace)
	}
	if !containsText(calls.texts(), "已打开 Codex App") || !containsText(calls.texts(), "thread-1") {
		t.Fatalf("app reply mismatch, messages=%#v", calls.texts())
	}
}

func TestCodexAppCommandKeepsLsOnOpenedWorkspace(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{workspace})
	staleWorkspace := t.TempDir()
	bindingKey := codexBindingKey("user-1", "codex")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.codexSessions.setActiveWorkspace(bindingKey, workspace)
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	h.setCodexBrowseWorkspace(bindingKey, staleWorkspace)
	h.SetCodexAppOpener(func(_ context.Context, _ string, _ string) error {
		return nil
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(127, "/cx app"))
	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(128, "/cx ls"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, filepath.Base(workspace)+" 会话") || !strings.Contains(text, "0. 未命名会话") {
		t.Fatalf("ls should show opened workspace session, messages=%#v", calls.texts())
	}
	if strings.Contains(text, filepath.Base(staleWorkspace)+" 会话") {
		t.Fatalf("ls should not stay on stale browse workspace, messages=%#v", calls.texts())
	}
}

func TestCodexAppFailureSuggestsCli(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{
			info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex-bin"},
		},
		threadID: "thread-1",
	}
	h.defaultName = "codex"
	h.agents["codex"] = ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetCodexAppOpener(func(_ context.Context, _ string, _ string) error {
		return errors.New("app unavailable")
	})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(130, "/cx app"))

	if !containsText(calls.texts(), "打开 Codex App 失败") || !containsText(calls.texts(), "/cx cli") {
		t.Fatalf("app failure reply should suggest /cx cli, messages=%#v", calls.texts())
	}
}
