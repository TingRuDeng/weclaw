package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestHandleCodexNewAcquireFailureRestoresPreviousThread(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.handoffErrors["thread-new"] = fmt.Errorf("handoff failed")
	oldIntent := h.codexSessions.controlIntent("thread-old")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(122, "/cx new"))

	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "thread-old" || pending || ag.threadID != "thread-old" {
		t.Fatalf("失败后状态 thread=%q pending=%v mapping=%q", thread, pending, ag.threadID)
	}
	if h.codexSessions.controlIntent("thread-old") != oldIntent || h.codexSessions.controlIntent("thread-new").Owner != codexControlUnclaimed {
		t.Fatalf("失败污染所有权: old=%#v new=%#v", h.codexSessions.controlIntent("thread-old"), h.codexSessions.controlIntent("thread-new"))
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "原会话已恢复") || !strings.Contains(text, "仍保留在 Codex 历史中") {
		t.Fatalf("失败回复=%q", text)
	}
}

func TestHandleCodexNewAcquireFailureClearsMappingWithoutPreviousThread(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	ag.handoffErrors["thread-new"] = fmt.Errorf("handoff failed")
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(124, "/cx new"))

	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if ag.clearCalledWith != conversationID || ag.threadID != "" || thread != "" || pending {
		t.Fatalf("无旧会话恢复失败: clear=%q mapping=%q store=(%q,%v)", ag.clearCalledWith, ag.threadID, thread, pending)
	}
	if !containsText(calls.texts(), "仍保留在 Codex 历史中") {
		t.Fatalf("失败回复未提示历史保留: %#v", calls.texts())
	}
}

func TestHandleCodexNewRestoreFailureFailsClosed(t *testing.T) {
	h, ag, _, _ := newCodexCreateFailureFixture(t)
	ag.handoffErrors["thread-new"] = fmt.Errorf("handoff failed")
	ag.useErr = fmt.Errorf("restore failed")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx new"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "移交结果未确认") || strings.Contains(text, "原会话已恢复") {
		t.Fatalf("恢复失败回复=%q", text)
	}
	if runtime := ag.threadBinding("thread-new").Runtime; runtime != agent.CodexRuntimeConflict {
		t.Fatalf("恢复失败 runtime=%q，期望持久 fail-closed", runtime)
	}
}

func TestHandleCodexNewResetFailureRestoresMapping(t *testing.T) {
	tests := []struct {
		name, created, wantReply, forbidden string
		resetErr                            error
	}{
		{name: "返回错误", created: "thread-new", resetErr: fmt.Errorf("start failed"), wantReply: "创建新的 Codex 会话失败", forbidden: "接管失败"},
		{name: "空会话 ID", wantReply: "Codex 未返回新会话 ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
			ag.resetSessionID, ag.resetErr = tt.created, tt.resetErr
			client, calls, closeServer := newRecordingILinkClient(t)
			defer closeServer()

			handleTestWeChatMessage(h, context.Background(), client, newTextMessage(126, "/cx new"))

			thread, pending := h.codexSessions.getThread(bindingKey, workspace)
			if thread != "thread-old" || pending || ag.threadID != "thread-old" || len(ag.handoffRequests()) != 0 {
				t.Fatalf("Reset 失败后 thread=%q pending=%v mapping=%q handoff=%d", thread, pending, ag.threadID, len(ag.handoffRequests()))
			}
			if !containsText(calls.texts(), tt.wantReply) {
				t.Fatalf("reply=%#v，期望包含 %q", calls.texts(), tt.wantReply)
			}
			if tt.forbidden != "" && containsText(calls.texts(), tt.forbidden) {
				t.Fatalf("reply=%#v，不应把创建错误描述为 %q", calls.texts(), tt.forbidden)
			}
		})
	}
}

func newCodexCreateFailureFixture(t *testing.T) (*Handler, *fakeCodexSessionCreateAgent, string, string) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexSessionCreateAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.resetSessionID = "thread-new"
	ag.fakeCodexThreadAgent.threadID = "thread-old"
	h.defaultName, h.agents["codex"] = "codex", ag
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	bindingKey := codexBindingKey("user-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-old")
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{
		routeUserID: "user-1", agentName: "codex", bindingKey: bindingKey,
		workspace: workspace, threadID: "thread-old",
	})
	return h, ag, workspace, bindingKey
}
