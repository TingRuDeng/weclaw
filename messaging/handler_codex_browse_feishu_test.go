package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuCodexCxLsSendsWorkspaceChoices(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "alpha")
	workspaceB := filepath.Join(root, "beta")
	writeLocalCodexSession(t, codexDir, "thread-a", workspaceA, "Alpha 会话", "2026-04-29T09:00:00Z")
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
	if choices[0].ID != "/cx cd 0" || choices[0].Label != "alpha" {
		t.Fatalf("first workspace choice=%#v, want /cx cd 0 alpha", choices[0])
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

func TestFeishuCodexWorkspaceChoiceSendsSessionChoices(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
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

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cx-workspace",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "/cx cd 0"},
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
	if len(choices) != 2 || choices[0].ID != "/cx switch 0" || choices[0].Label != "会话 A" {
		t.Fatalf("session choices=%#v, want switch choices", choices)
	}
}

func TestFeishuCodexInvalidWorkspaceReturnsTextError(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "weclaw")
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
