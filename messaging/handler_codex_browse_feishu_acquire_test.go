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

// TestFeishuCodexSingleSessionRuntimeFailureKeepsCommittedSelection 验证运行通道失败不撤销窗口选择。
func TestFeishuCodexSingleSessionRuntimeFailureKeepsCommittedSelection(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir, root := t.TempDir(), t.TempDir()
	oldWorkspace := filepath.Join(root, "old")
	targetWorkspace := filepath.Join(root, "weclaw")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-b", targetWorkspace, "会话 B", "2026-07-15T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.handoffErrors["thread-b"] = errors.New("探测失败")
	h.defaultName, h.agents["codex"] = "codex", ag
	bindingKey := codexBindingKey("ou_user", "codex")
	h.ensureCodexSessions().setThread(bindingKey, oldWorkspace, "thread-a")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, oldWorkspace)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		MessageID: "feishu-cx-single-failure", Text: "/cx cd weclaw",
	}, reply)

	active, _ := h.ensureCodexSessions().getActiveWorkspace(bindingKey)
	targetThread, pending := h.ensureCodexSessions().getThread(bindingKey, targetWorkspace)
	if len(reply.Choices) != 0 || len(reply.Texts) != 1 ||
		!strings.Contains(reply.Texts[0], "已进入工作空间并绑定唯一会话") ||
		!strings.Contains(reply.Texts[0], "运行通道: 暂不可用") {
		t.Fatalf("choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
	if active != targetWorkspace || targetThread != "thread-b" || pending {
		t.Fatalf("active=%q target=%q pending=%t", active, targetThread, pending)
	}
}
