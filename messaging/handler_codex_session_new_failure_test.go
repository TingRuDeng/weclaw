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

func TestHandleCodexNewRuntimeFailureKeepsNewThreadAndOwner(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.handoffErrors["thread-new"] = fmt.Errorf("handoff failed")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(122, "/cx new"))

	thread, pending := h.codexSessions.getThread(bindingKey, workspace)
	if thread != "thread-new" || pending || ag.threadID != "thread-new" {
		t.Fatalf("运行通道失败后状态 thread=%q pending=%v mapping=%q", thread, pending, ag.threadID)
	}
	if !h.codexSessions.isPendingFirstTurn(bindingKey, workspace, "thread-new") {
		t.Fatal("/cx new 创建的 thread 在首条消息前必须持久标记 pending-first-turn")
	}
	if old := h.codexSessions.controlIntent("thread-old"); old.Owner != codexControlDesktop {
		t.Fatalf("old owner=%#v", old)
	}
	if target := h.codexSessions.controlIntent("thread-new"); target.Owner != codexControlRemote || target.RouteBindingKey != bindingKey {
		t.Fatalf("target owner=%#v", target)
	}
	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已创建并接管") || strings.Contains(text, "原会话已恢复") {
		t.Fatalf("回复=%q", text)
	}
}

func TestHandleCodexNewRuntimeFailureKeepsMappingWithoutPreviousThread(t *testing.T) {
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

	thread, pending := h.codexSessions.getThread(codexBindingKey("user-1", "codex"), workspace)
	if ag.clearCalledWith != "" || ag.threadID != "thread-new" || thread != "thread-new" || pending {
		t.Fatalf("mapping clear=%q runtime=%q store=(%q,%v)", ag.clearCalledWith, ag.threadID, thread, pending)
	}
	if !containsText(calls.texts(), "已创建并接管") {
		t.Fatalf("回复未说明接管状态: %#v", calls.texts())
	}
}

func TestHandleCodexNewRuntimeFailureDoesNotBlockRemoteWrites(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.handoffErrors["thread-new"] = fmt.Errorf("handoff failed")
	client, calls, closeServer := newRecordingILinkClient(t)
	defer closeServer()

	handleTestWeChatMessage(h, context.Background(), client, newTextMessage(125, "/cx new"))

	text := strings.Join(calls.texts(), "\n")
	if !strings.Contains(text, "已创建并接管") {
		t.Fatalf("回复=%q", text)
	}
	assertCodexCreateRuntimeFailureAllowsRemoteWrite(t, h, ag, workspace, bindingKey)
}

func assertCodexCreateRuntimeFailureAllowsRemoteWrite(t *testing.T, h *Handler, ag *fakeCodexSessionCreateAgent, workspace string, bindingKey string) {
	t.Helper()
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	conversationID := buildCodexConversationID("user-1", "codex", workspace)
	opts := codexAgentTaskOptions{
		ctx: context.Background(), userID: "user-1", routeUserID: "user-1",
		reply: reply, agentName: "codex", message: "继续任务", agent: ag, progressCfg: cfg,
		route: codexConversationRoute{bindingKey: bindingKey, workspaceRoot: workspace, conversationID: conversationID, threadID: "thread-new"},
	}
	h.startCodexAgentTask(opts)
	waitUntil(t, func() bool { return ag.runCallSnapshot() == 1 })
	if h.codexSessions.isPendingFirstTurn(bindingKey, workspace, "thread-new") {
		t.Fatal("Codex 接受首个 turn 后必须立即清除 pending-first-turn")
	}
	if text := strings.Join(reply.Texts, "\n"); strings.Contains(text, "运行通道暂不可用") {
		t.Fatalf("remote owner 的普通消息被技术恢复失败阻断: %q", text)
	}
}

func TestCreateAndAcquireCodexSessionRestoresAfterHardFailureWithCanceledParent(t *testing.T) {
	h, ag, workspace, bindingKey := newCodexCreateFailureFixture(t)
	ag.rejectCanceledUse = true
	// 运行通道失败不再回滚新会话；用本地持久化硬失败进入创建补偿路径。
	h.codexSessions.SetFilePath(t.TempDir())
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
	if err == nil || errors.Is(err, errCodexSessionAcquireUncertain) {
		t.Fatalf("err=%v，期望可确定的本地持久化失败", err)
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
