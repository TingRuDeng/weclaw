package messaging

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexSharedHostMessageNeverRequestsOwnerChoice(t *testing.T) {
	h, ag, opts, _ := liveMessageFixture(t, false)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	opts.platform, opts.reply = platform.PlatformFeishu, reply

	h.startCodexAgentTask(opts)

	waitUntil(t, func() bool {
		runCalls, _ := ag.runCallSnapshot()
		return runCalls == 1
	})
	if len(reply.Choices) != 0 {
		t.Fatalf("shared host triggered owner choice card: %#v", reply.Choices)
	}
}

func TestCodexUnknownClientSnapshotDoesNotVetoSharedHostTurn(t *testing.T) {
	h, ag, opts, route := liveMessageFixture(t, false)
	ag.setBindingRuntime(agent.CodexRuntimeUnknown)
	snapshot := h.ensureCodexSessions().remoteSelectionSnapshot(route.bindingKey, route.threadID)
	if _, err := h.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: route.bindingKey, WorkspaceRoot: route.workspaceRoot,
		TargetThreadID: route.threadID, ConversationID: route.conversationID,
		PendingFirstTurn: true, Expected: snapshot,
	}); err != nil {
		t.Fatal(err)
	}

	h.startCodexAgentTask(opts)

	waitUntil(t, func() bool {
		runCalls, _ := ag.runCallSnapshot()
		return runCalls == 1
	})
	_, request := ag.runCallSnapshot()
	if !request.Runtime.PendingFirstTurn {
		t.Fatal("pending first turn binding was not propagated")
	}
	if text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n"); strings.Contains(text, "控制权") {
		t.Fatalf("shared host message was rejected by legacy ownership: %q", text)
	}
}

func TestCodexPreparingFollowerRejectsOrdinaryMessageBeforeRunOrSteer(t *testing.T) {
	for _, active := range []bool{false, true} {
		active := active
		t.Run(map[bool]string{false: "idle", true: "active"}[active], func(t *testing.T) {
			h, ag, opts, route := liveMessageFixture(t, active)
			deliveryRoute := platform.DeliveryRoute{
				Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "chat-a", ReplyToID: "message-a",
			}
			initial := h.ensureCodexSessions().remoteSelectionSnapshot(route.bindingKey, route.threadID)
			if _, err := h.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
				BindingKey: route.bindingKey, WorkspaceRoot: route.workspaceRoot,
				TargetThreadID: route.threadID, ConversationID: route.conversationID,
				SetFollower: true,
				Follower: &codexFrontendFollower{
					WorkspaceRoot: route.workspaceRoot, ThreadID: route.threadID,
					ActorUserID: opts.userID, AuthorizedIdentity: opts.userID, DeliveryRoute: deliveryRoute,
				},
				FollowerTurnID: "turn-1", FollowerTurnInitialized: active,
				Expected: initial,
			}); err != nil {
				t.Fatal(err)
			}

			h.startCodexAgentTask(opts)

			waitUntil(t, func() bool {
				runCalls, _ := ag.runCallSnapshot()
				return len(opts.reply.(*platformtest.Replier).Texts) > 0 || runCalls > 0 || ag.steerTurnID != ""
			})
			runCalls, _ := ag.runCallSnapshot()
			if runCalls != 0 || ag.steerTurnID != "" {
				t.Fatalf("preparing follower wrote to Codex: run=%d steer=%q", runCalls, ag.steerTurnID)
			}
			text := strings.Join(opts.reply.(*platformtest.Replier).Texts, "\n")
			if !strings.Contains(text, "正在切换中") || !strings.Contains(text, "请稍后") {
				t.Fatalf("reply=%q, want friendly attach-in-progress notice", text)
			}
		})
	}
}
