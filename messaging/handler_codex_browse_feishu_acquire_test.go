package messaging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

// TestFeishuCodexSingleSessionRuntimeFailureKeepsPreviousSelection 验证运行通道失败不提交候选窗口选择。
func TestFeishuCodexSingleSessionRuntimeFailureKeepsPreviousSelection(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir, root := t.TempDir(), t.TempDir()
	oldWorkspace := filepath.Join(root, "old")
	targetWorkspace := filepath.Join(root, "weclaw")
	writeLocalCodexSession(t, codexDir, "thread-b", targetWorkspace, "会话 B", "2026-07-15T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.handoffErrors["thread-b"] = errors.New("探测失败")
	h.defaultName, h.agents["codex"] = "codex", ag
	bindingKey := codexBindingKey("ou_user", "codex")
	h.ensureCodexSessions().setThread(bindingKey, oldWorkspace, "thread-a")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, oldWorkspace)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.handleMessageForTest(context.Background(), authorizeIncomingMessageForTest(t, platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		MessageID: "feishu-cx-single-failure", Text: "/cx cd weclaw",
	}, "ou_user"), reply)

	active, _ := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	targetThread, pending := h.ensureCodexSessions().getThread(bindingKey, targetWorkspace)
	if len(reply.Choices) != 0 || len(reply.Texts) != 1 ||
		!strings.Contains(reply.Texts[0], "原会话已保留") {
		t.Fatalf("choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
	if active != oldWorkspace || targetThread != "" || pending {
		t.Fatalf("active=%q target=%q pending=%t", active, targetThread, pending)
	}
}
