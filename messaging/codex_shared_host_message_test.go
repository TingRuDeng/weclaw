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
	snapshot := h.codexSessions.remoteSelectionSnapshot(route.bindingKey, route.threadID)
	if _, err := h.codexSessions.commitRemoteSelection(codexRemoteSelectionUpdate{
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
