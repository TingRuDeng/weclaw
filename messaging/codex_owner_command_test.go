package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestCodexOwnerStatusReturnsCardState(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	result, handled := h.dispatchCodexUtilityCommand(runtime)
	if !handled || !result.ShowCard || !strings.Contains(result.Reply, "控制方: 未认领") {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
	if ag.bindCalls != 1 {
		t.Fatalf("探测次数=%d，期望 1", ag.bindCalls)
	}
}

func TestCodexOwnerStatusTimeoutReleasesThreadLock(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.inspectRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runtime.ctx = ctx

	result := h.handleCodexOwnerCommand(runtime)
	if !strings.Contains(result.Reply, "控制权查询超时") ||
		!strings.Contains(result.Reply, "运行位置未确认") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexThreadLockReusable(t, h, "thread-1")
}

func assertCodexThreadLockReusable(t *testing.T, h *Handler, threadID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	unlock, err := h.lockCodexThreadControlContext(ctx, threadID)
	if err != nil {
		t.Fatalf("thread 控制锁无法复用: %v", err)
	}
	unlock()
}

func TestFeishuCodexOwnerStatusUsesChoiceCard(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	msg := platform.IncomingMessage{Platform: platform.PlatformFeishu, UserID: "user-1"}
	result := cardNavigationResult("Codex 会话控制")
	if !h.sendFeishuCodexOwnerChoices(feishuCodexSessionCommandRequest{
		ctx: context.Background(), message: msg, routeUserID: "route-1",
		reply: reply, trimmed: "/cx owner", result: result,
	}) {
		t.Fatal("未发送所有权选择卡片")
	}
	if len(reply.Choices) != 1 || len(reply.Choices[0].Choices) != 2 {
		t.Fatalf("choices=%#v", reply.Choices)
	}
	if reply.Choices[0].Choices[0].ID != "/cx owner remote" ||
		reply.Choices[0].Choices[1].ID != "/cx owner desktop" {
		t.Fatalf("choices=%#v", reply.Choices[0].Choices)
	}
}

func TestFeishuCodexOwnerActionDoesNotCreateAnotherChoiceCard(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	request := feishuCodexSessionCommandRequest{
		ctx:         context.Background(),
		message:     platform.IncomingMessage{Platform: platform.PlatformFeishu, UserID: "user-1"},
		routeUserID: "route-1", reply: reply,
		trimmed: "/cx owner remote", result: cardNavigationResult("已移交"),
	}
	if h.sendFeishuCodexOwnerChoices(request) {
		t.Fatal("控制权动作完成后不应再次生成选择卡")
	}
}

func codexOwnerCommandFixture(t *testing.T) (*Handler, *fakeCodexLiveAgent, codexSessionCommandRuntime) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{ThreadID: "thread-1"})
	bindingKey := codexBindingKey("route-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	runtime := codexSessionCommandRuntime{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		fields: []string{"/cx", "owner"}, agentName: "codex", agent: ag,
		bindingKey: bindingKey, workspaceRoot: workspace,
		req: codexSessionCommandRequest{
			ActorUserID: "user-1", RouteUserID: "route-1",
			Platform: platform.PlatformWeChat,
			Reply:    platformtest.NewReplier(platform.Capabilities{Text: true}),
		},
	}
	return h, ag, runtime
}
