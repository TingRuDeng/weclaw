package messaging

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
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
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.handoffErrors["thread-new"] = fmt.Errorf("handoff failed")
	ag.useErr = fmt.Errorf("restore failed")
	oldIntent := h.codexSessions.controlIntent("thread-old")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx new"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "移交结果未确认") || strings.Contains(text, "原会话已恢复") {
		t.Fatalf("恢复失败回复=%q", text)
	}
	if oldRuntime, newRuntime := ag.threadBinding("thread-old").Runtime, ag.threadBinding("thread-new").Runtime; oldRuntime != agent.CodexRuntimeConflict || newRuntime != agent.CodexRuntimeConflict {
		t.Fatalf("恢复失败 runtime old=%q new=%q，期望均持久 fail-closed", oldRuntime, newRuntime)
	}
	marks, _ := ag.conflictSnapshot()
	if !reflect.DeepEqual(marks, []string{"thread-new", "thread-old"}) || ag.threadID != "thread-old" {
		t.Fatalf("conflict marks=%#v mapping=%q，期望先 B 后 A", marks, ag.threadID)
	}
	markIntents := ag.conflictIntentSnapshot()
	if len(markIntents) != 2 || markIntents[0].Owner != agent.CodexControlUnclaimed || markIntents[1] != agentControlIntent(oldIntent) {
		t.Fatalf("conflict intents=%#v，期望 B 未认领、A 保留原意图", markIntents)
	}
	if h.codexSessions.controlIntent("thread-old") != oldIntent || h.codexSessions.controlIntent("thread-new").Owner != codexControlUnclaimed {
		t.Fatalf("fail-closed 污染 intent: old=%#v new=%#v", h.codexSessions.controlIntent("thread-old"), h.codexSessions.controlIntent("thread-new"))
	}
	if selected, pending := h.codexSessions.getThread(bindingKey, workspace); selected != "thread-old" || pending {
		t.Fatalf("fail-closed 污染选择: thread=%q pending=%t", selected, pending)
	}
	assertCodexCreateConflictBlocksUntilExplicitSelection(t, h, ag, workspace, bindingKey)
}

func assertCodexCreateConflictBlocksUntilExplicitSelection(t *testing.T, h *Handler, ag *fakeCodexSessionCreateAgent, workspace string, bindingKey string) {
	t.Helper()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	opts := codexAgentTaskOptions{
		ctx: context.Background(), userID: "user-1", routeUserID: "user-1",
		reply: reply, agentName: "codex", message: "继续任务", agent: ag, progressCfg: cfg,
		route: codexConversationRoute{bindingKey: bindingKey, workspaceRoot: workspace, conversationID: conversationID, threadID: "thread-old"},
	}
	h.startCodexAgentTask(opts)
	if len(reply.Texts) == 0 || ag.runCallSnapshot() != 0 {
		t.Fatalf("冲突态未拒绝普通消息: texts=%#v run=%d", reply.Texts, ag.runCallSnapshot())
	}
	delete(ag.handoffErrors, "thread-new")
	result := h.handleCodexSessionCommandForRoute(context.Background(), codexSessionCommandRequest{
		ActorUserID: "user-1", RouteUserID: "user-1", Trimmed: "/cx switch thread-new",
	})
	if !strings.Contains(result, "已切换并接管") {
		t.Fatalf("显式重新选择失败: %q", result)
	}
	opts.route.threadID = "thread-new"
	opts.reply = platformtest.NewReplier(platform.Capabilities{Text: true})
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.runCallSnapshot() == 1 })
}

func TestCreateAndAcquireCodexSessionRestoresWithCanceledParent(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.rejectCanceledContext = true
	ag.rejectCanceledUse = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conversationID := buildCodexConversationID("user-1", "codex", workspace)

	_, err := h.createAndAcquireCodexSessionWithBindingLocked(codexSessionCreateRequest{
		acquire: codexSessionAcquireRequest{
			ctx: ctx, actorUserID: "user-1", routeUserID: "user-1", agentName: "codex", agent: ag,
			route: codexConversationRoute{bindingKey: bindingKey, workspaceRoot: workspace, conversationID: conversationID},
		},
	})

	_, useContextErrors := ag.conflictSnapshot()
	if !errors.Is(err, context.Canceled) || errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("err=%v，期望确定的 parent canceled", err)
	}
	if ag.threadID != "thread-old" || len(useContextErrors) != 1 || useContextErrors[0] != nil {
		t.Fatalf("mapping=%q useCtx=%#v，恢复必须脱离 parent cancel", ag.threadID, useContextErrors)
	}
}

func TestCreateAndAcquireCodexSessionResetFailureRestoresWithCanceledParent(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.resetErr = context.Canceled
	ag.rejectCanceledUse = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conversationID := buildCodexConversationID("user-1", "codex", workspace)

	_, err := h.createAndAcquireCodexSessionWithBindingLocked(codexSessionCreateRequest{
		acquire: codexSessionAcquireRequest{
			ctx: ctx, actorUserID: "user-1", routeUserID: "user-1", agentName: "codex", agent: ag,
			route: codexConversationRoute{bindingKey: bindingKey, workspaceRoot: workspace, conversationID: conversationID},
		},
	})

	_, useContextErrors := ag.conflictSnapshot()
	if !errors.Is(err, context.Canceled) || errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("err=%v，期望 Reset 的确定 parent canceled", err)
	}
	if ag.threadID != "thread-old" || len(useContextErrors) != 1 || useContextErrors[0] != nil {
		t.Fatalf("mapping=%q useCtx=%#v，Reset 失败恢复必须脱离 parent cancel", ag.threadID, useContextErrors)
	}
}

func TestMarkCodexSessionCreateConflictContinuesAfterEachMarkFailure(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	newMarkErr := errors.New("mark new failed")
	oldMarkErr := errors.New("mark old failed")
	ag.markErrors["thread-new"] = newMarkErr
	ag.markErrors["thread-old"] = oldMarkErr
	conversationID := buildCodexConversationID("user-1", "codex", workspace)

	err := h.markCodexSessionCreateConflict(codexSessionCreateFailureRequest{
		createRequest: codexSessionCreateRequest{acquire: codexSessionAcquireRequest{
			ctx: context.Background(), agent: ag,
			route: codexConversationRoute{bindingKey: bindingKey, workspaceRoot: workspace, conversationID: conversationID},
		}},
		previousThreadID: "thread-old",
		createdThread:    "thread-new",
	})

	marks, _ := ag.conflictSnapshot()
	if !errors.Is(err, newMarkErr) || !errors.Is(err, oldMarkErr) {
		t.Fatalf("err=%v，期望聚合两个标记错误", err)
	}
	if !reflect.DeepEqual(marks, []string{"thread-new", "thread-old"}) || ag.threadID != "thread-old" {
		t.Fatalf("marks=%#v mapping=%q，标记失败仍须继续到 A", marks, ag.threadID)
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
