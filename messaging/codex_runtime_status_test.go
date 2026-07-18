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

func TestCodexStatusReturnsSharedHostState(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	result, handled := h.dispatchCodexUtilityCommand(runtime)
	if !handled || result.ShowCard || !strings.Contains(result.Reply, "Codex 状态") ||
		!strings.Contains(result.Reply, "窗口绑定: 已绑定") {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
	if ag.bindCalls != 1 {
		t.Fatalf("探测次数=%d，期望 1", ag.bindCalls)
	}
}

func TestCodexStatusTimeoutReleasesThreadLock(t *testing.T) {
	h, ag, runtime := codexRuntimeStatusFixture(t)
	ag.inspectRelease = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runtime.ctx = ctx

	result := h.renderCodexStatus(runtime)
	if !strings.Contains(result.Reply, "共享服务状态: 暂不可用") {
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

func TestRemovedCodexOwnerReturnsSessionHelp(t *testing.T) {
	h, _, runtime := codexRuntimeStatusFixture(t)
	runtime.fields = []string{"/cx", "owner", "desktop"}
	result := h.dispatchCodexSessionCommand(runtime)
	if !strings.Contains(result.Reply, "Codex 会话命令") || strings.Contains(result.Reply, "无需释放 Codex 控制权") {
		t.Fatalf("reply=%q", result.Reply)
	}
	threadID, pending := h.codexSessions.getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || threadID != "thread-1" {
		t.Fatalf("binding changed: thread=%q pending=%v", threadID, pending)
	}
}

func codexRuntimeStatusFixture(t *testing.T) (*Handler, *fakeCodexLiveAgent, codexSessionCommandRuntime) {
	t.Helper()
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-1"})
	bindingKey := codexBindingKey("route-1", "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-1")
	runtime := codexSessionCommandRuntime{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		fields: []string{"/cx", "status"}, agentName: "codex", agent: ag,
		bindingKey: bindingKey, workspaceRoot: workspace,
		req: codexSessionCommandRequest{
			ActorUserID: "user-1", RouteUserID: "route-1",
			Platform: platform.PlatformWeChat,
			Reply:    platformtest.NewReplier(platform.Capabilities{Text: true}),
		},
	}
	return h, ag, runtime
}
