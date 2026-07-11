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

func TestParseClaudeAssistantModelReadsExplicitEffort(t *testing.T) {
	status, ok := parseClaudeAssistantModel([]byte(
		`{"type":"assistant","effortLevel":"high","message":{"model":"claude-opus-4-1"}}`,
	))
	if !ok || status.Model != "claude-opus-4-1" || status.Effort != "high" {
		t.Fatalf("status=%#v ok=%t，期望读取明确记录的模型和推理强度", status, ok)
	}
}

func TestFeishuClaudeSessionCardShowsActualModelStatus(t *testing.T) {
	h := NewHandler(nil, nil)
	claudeDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "claude-project")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	writeLocalClaudeSession(t, claudeDir, "session-card", workspace, "卡片会话", "2026-04-29T09:00:00Z")
	appendLocalClaudeAssistantModel(t, claudeDir, workspace, "session-card", "claude-sonnet-4-5")
	h.SetClaudeLocalSessionDir(claudeDir)
	h.defaultName = "claude"
	h.agents["claude"] = &fakeClaudeSessionAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "claude", Type: "cli", Command: "claude"},
	}}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-cc-switch",
		RawCommand: &platform.CardAction{
			Action: "choice",
			Value:  map[string]string{"choice": "/cc switch 0"},
		},
	}, reply)

	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "模型: claude-sonnet-4-5") {
		t.Fatalf("card switch should show actual claude model, texts=%#v", reply.Texts)
	}
	if !strings.Contains(reply.Texts[0], "推理强度: "+unknownSessionModelValue) {
		t.Fatalf("card switch should show unknown unrecorded effort, texts=%#v", reply.Texts)
	}
}
