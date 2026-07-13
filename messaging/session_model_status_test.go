package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestFeishuClaudeSessionCardShowsACPModelStatus(t *testing.T) {
	h, ag, workspace := newClaudeACPNavigationHandler(t)
	ag.catalogSessions = []agent.ClaudeSession{{ID: "session-card", Cwd: workspace, Title: "卡片会话"}}
	ag.sessionConfig = agent.ClaudeSessionConfig{Model: "claude-sonnet-4-5", Effort: "high"}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: "feishu-cc-switch",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": "/cc switch 0"}},
	}, reply)

	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "模型: claude-sonnet-4-5") {
		t.Fatalf("texts=%#v，期望显示 ACP 返回的 Claude 模型", reply.Texts)
	}
	if !strings.Contains(reply.Texts[0], "推理强度: high") {
		t.Fatalf("texts=%#v，期望显示 ACP 返回的推理强度", reply.Texts)
	}
}
