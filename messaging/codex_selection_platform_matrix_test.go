package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexSelectionProbeReplier struct {
	*platformtest.Replier
	beforeText func(string)
}

func (r *codexSelectionProbeReplier) SendText(ctx context.Context, value string) error {
	if r.beforeText != nil {
		r.beforeText(value)
	}
	return r.Replier.SendText(ctx, value)
}

func TestCodexSelectionBindsForWeChatAndFeishu(t *testing.T) {
	tests := []struct {
		name     string
		platform platform.PlatformName
		actor    string
		route    string
		account  string
		metadata map[string]string
	}{
		{name: "微信", platform: platform.PlatformWeChat, actor: "wx-actor", route: "wx-actor", account: "wx-bot"},
		{name: "飞书", platform: platform.PlatformFeishu, actor: "fs-actor", route: "feishu:tenant:group:chat:root", account: "fs-bot",
			metadata: map[string]string{feishuSessionMetadataKey: "feishu:tenant:group:chat:root"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, ag, workspaceA, workspaceB := newPlatformBindingFixture(t, test.route)
			bindingKey := codexBindingKey(test.route, "codex")
			conversationID := buildCodexConversationID(test.route, "codex", workspaceB)
			reply := &codexSelectionProbeReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true})}
			reply.beforeText = func(_ string) {
				active, _ := h.codexSessions.getActiveWorkspace(bindingKey)
				threadID, pending := h.codexSessions.getThread(bindingKey, workspaceB)
				if active != workspaceB || pending || threadID != "thread-b" {
					t.Fatalf("reply before binding commit: active=%q thread=%q pending=%v", active, threadID, pending)
				}
				requests := ag.handoffRequests()
				if len(requests) != 1 || requests[0].Ref.ThreadID != "thread-b" ||
					requests[0].Ref.ConversationID != conversationID || requests[0].Intent.RouteKey != bindingKey {
					t.Fatalf("reply before shared host bind: %#v", requests)
				}
			}

			h.HandleMessage(context.Background(), platform.IncomingMessage{
				Platform: test.platform, AccountID: test.account, UserID: test.actor,
				MessageID: test.name + "-select", Text: "/cx switch thread-b", Metadata: test.metadata,
			}, reply)
			if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "已切换并绑定") {
				t.Fatalf("texts=%#v", reply.Texts)
			}
			if strings.Contains(reply.Texts[0], test.route) || strings.Contains(reply.Texts[0], test.account) {
				t.Fatalf("reply leaked route/account: %q", reply.Texts[0])
			}
			if active, _ := h.codexSessions.getActiveWorkspace(bindingKey); active != workspaceB {
				t.Fatalf("active=%q want=%q (old=%q)", active, workspaceB, workspaceA)
			}
			if _, active := h.activeTask(conversationID); active {
				t.Fatal("idle target created observer")
			}
		})
	}
}

func newPlatformBindingFixture(t *testing.T, routeUserID string) (*Handler, *fakeCodexLiveAgent, string, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	root := t.TempDir()
	workspaceA, workspaceB := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.SetAllowedWorkspaceRoots([]string{root})
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	h.SetCodexLocalSessionDir(t.TempDir())
	h.defaultName = "codex"
	bindingKey := codexBindingKey(routeUserID, "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	h.codexSessions.setActiveWorkspace(bindingKey, workspaceA)
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	for _, threadID := range []string{"thread-a", "thread-b"} {
		ag.setThreadBinding(threadID, agent.CodexThreadBinding{
			Runtime: agent.CodexRuntimeWeClaw,
			State:   agent.CodexThreadState{ThreadID: threadID},
		})
	}
	h.agents["codex"] = ag
	return h, ag, workspaceA, workspaceB
}
