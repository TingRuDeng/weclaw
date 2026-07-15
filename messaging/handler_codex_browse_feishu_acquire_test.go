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

// TestFeishuCodexSingleSessionAcquireFailureKeepsOriginalState 验证单会话自动接管失败不发卡片也不污染原状态。
func TestFeishuCodexSingleSessionAcquireFailureKeepsOriginalState(t *testing.T) {
	h := NewHandler(nil, nil)
	codexDir, root := t.TempDir(), t.TempDir()
	oldWorkspace := filepath.Join(root, "old")
	targetWorkspace := filepath.Join(root, "weclaw")
	h.SetAllowedWorkspaceRoots([]string{root})
	writeLocalCodexSession(t, codexDir, "thread-b", targetWorkspace, "会话 B", "2026-07-15T09:00:00Z")
	h.SetCodexLocalSessionDir(codexDir)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.handoffErrors["thread-b"] = errors.New("探测失败")
	h.defaultName, h.agents["codex"] = "codex", ag
	bindingKey := codexBindingKey("ou_user", "codex")
	h.codexSessions.setThread(bindingKey, oldWorkspace, "thread-a")
	h.codexSessions.setActiveWorkspace(bindingKey, oldWorkspace)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "ou_user", agentName: "codex", bindingKey: bindingKey,
		workspace: oldWorkspace, threadID: "thread-a",
	})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user",
		MessageID: "feishu-cx-single-failure", Text: "/cx cd weclaw",
	}, reply)

	active, _ := h.codexSessions.getActiveWorkspace(bindingKey)
	targetThread, pending := h.codexSessions.getThread(bindingKey, targetWorkspace)
	if len(reply.Choices) != 0 || len(reply.Texts) != 1 ||
		!strings.Contains(reply.Texts[0], "切换并接管 Codex 会话失败") {
		t.Fatalf("choices=%#v texts=%#v", reply.Choices, reply.Texts)
	}
	if active != oldWorkspace || targetThread != "" || pending ||
		h.codexSessions.controlIntent("thread-a").Owner != codexControlRemote ||
		h.codexSessions.controlIntent("thread-b").Owner != codexControlUnclaimed {
		t.Fatalf("active=%q target=%q pending=%t intents=(%#v,%#v)", active, targetThread, pending,
			h.codexSessions.controlIntent("thread-a"), h.codexSessions.controlIntent("thread-b"))
	}
}
