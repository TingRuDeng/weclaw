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

// TestFeishuCodexShortNavigationKeepsCardState 验证短编号导航不会丢失结构化卡片状态。
func TestFeishuCodexShortNavigationKeepsCardState(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir := t.TempDir()
	root := t.TempDir()
	workspace := filepath.Join(root, "alpha")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-a", workspace, "会话 A", "2026-04-29T09:00:00Z")
	writeLocalCodexSession(t, codexDir, "thread-b", workspace, "会话 B", "2026-04-29T08:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	h.defaultName = "codex"
	h.agents["codex"] = &fakeCodexThreadAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"},
	}}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), shortNavigationMessage("short-enter", "/cx 0"), reply)
	if len(reply.Choices) != 1 || !strings.Contains(reply.Choices[0].Prompt, "alpha 会话") {
		t.Fatalf("enter choices=%#v, want session card", reply.Choices)
	}

	h.HandleMessage(context.Background(), shortNavigationMessage("short-back", "/cx .."), reply)
	if len(reply.Choices) != 2 || !strings.Contains(reply.Choices[1].Prompt, "Codex 工作空间") {
		t.Fatalf("back choices=%#v, want workspace card", reply.Choices)
	}
}

// shortNavigationMessage 构造独立消息 ID 的飞书短导航输入。
func shortNavigationMessage(messageID string, text string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform: platform.PlatformFeishu,
		UserID:   "ou_user", MessageID: messageID, Text: text,
	}
}
