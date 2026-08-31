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

func TestFeishuCodexNewUsesStatusCard(t *testing.T) {
	h, _, workspace, sessionKey := newFeishuCodexNewCardFixture(t)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true, Streaming: true})

	h.handleMessageForTest(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: "cx-new-card", Text: "/cx new",
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, reply)

	if len(reply.Texts) != 0 || reply.OpenStreamCalls != 1 || reply.Stream.Completed == "" {
		t.Fatalf("texts=%#v streamCalls=%d completed=%q, want one completed status card", reply.Texts, reply.OpenStreamCalls, reply.Stream.Completed)
	}
	if reply.Stream.Options.Title != "Codex 会话" {
		t.Fatalf("title=%q, want Codex 会话", reply.Stream.Options.Title)
	}
	for _, want := range []string{
		"已创建并绑定。",
		"工作空间: " + filepath.Base(workspace),
		"模型: " + unknownSessionModelValue,
		"推理强度: " + unknownSessionModelValue,
	} {
		if !strings.Contains(reply.Stream.Completed, want) {
			t.Fatalf("completed=%q, missing %q", reply.Stream.Completed, want)
		}
	}
}

func TestFeishuCodexNewFallsBackToTextWhenStatusCardCannotOpen(t *testing.T) {
	h, _, workspace, sessionKey := newFeishuCodexNewCardFixture(t)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true, Streaming: true})
	reply.OpenStreamErr = errors.New("cardkit unavailable")

	h.handleMessageForTest(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: "cx-new-card-fallback", Text: "/cx new",
		Metadata: map[string]string{feishuSessionMetadataKey: sessionKey},
	}, reply)

	if reply.OpenStreamCalls != 1 || len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "已创建并绑定。") {
		t.Fatalf("streamCalls=%d texts=%#v, card failure must fall back to success text", reply.OpenStreamCalls, reply.Texts)
	}
	threadID, pending := h.ensureCodexSessions().getThread(codexBindingKey(sessionKey, "codex"), workspace)
	if threadID != "thread-new" || pending {
		t.Fatalf("thread=%q pending=%t, presentation fallback must preserve committed binding", threadID, pending)
	}
}

func newFeishuCodexNewCardFixture(t *testing.T) (*Handler, *fakeCodexSessionCreateAgent, string, string) {
	t.Helper()
	workspace := t.TempDir()
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	h := NewHandler(nil, nil)
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	return h, ag, workspace, "feishu:tenant:dm:oc_1:ou_user"
}
