package messaging

import (
	"context"
	"path/filepath"
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
	if !handled || result.ShowCard {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
	for _, want := range []string{
		"Codex 状态",
		"工作空间: " + filepath.Base(runtime.workspaceRoot),
		"会话: 未命名会话",
		"任务: 空闲",
		"运行: 正常",
	} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply=%q, want %q", result.Reply, want)
		}
	}
	for _, obsolete := range []string{"窗口绑定:", "写入服务:", "运行模式:", "窗口角色:", "说明:"} {
		if strings.Contains(result.Reply, obsolete) {
			t.Fatalf("reply=%q, should omit %q", result.Reply, obsolete)
		}
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
	if !strings.Contains(result.Reply, "任务: 未确认") ||
		!strings.Contains(result.Reply, "运行: 暂不可用") {
		t.Fatalf("reply=%q", result.Reply)
	}
	assertCodexThreadLockReusable(t, h, "thread-1")
}

func TestCompactCodexRuntimeStatusLinesPreserveTaskAndRuntimeFailures(t *testing.T) {
	tests := []struct {
		name       string
		resolution codexRuntimeResolution
		task       string
		runtime    string
	}{
		{
			name:       "idle",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw}},
			task:       "任务: 空闲",
			runtime:    "运行: 正常",
		},
		{
			name: "active",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{
				Runtime: agent.CodexRuntimeWeClaw,
				State:   agent.CodexThreadState{Active: true},
			}},
			task:    "任务: 正在执行",
			runtime: "运行: 正常",
		},
		{
			name:       "conflict",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeConflict}},
			task:       "任务: 空闲",
			runtime:    "运行: 异常（写入冲突）",
		},
		{
			name:       "desktop",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeDesktop}},
			task:       "任务: 空闲",
			runtime:    "运行: 异常（旧版 Codex Desktop bridge）",
		},
		{
			name:       "unknown",
			resolution: codexRuntimeResolution{Binding: agent.CodexThreadBinding{Runtime: agent.CodexRuntimeUnknown}},
			task:       "任务: 空闲",
			runtime:    "运行: 未确认",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, runtime := compactCodexRuntimeStatusLines(test.resolution)
			if task != test.task || runtime != test.runtime {
				t.Fatalf("task=%q runtime=%q, want task=%q runtime=%q", task, runtime, test.task, test.runtime)
			}
		})
	}
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
	threadID, pending := h.ensureCodexSessions().getThread(runtime.bindingKey, runtime.workspaceRoot)
	if pending || threadID != "thread-1" {
		t.Fatalf("binding changed: thread=%q pending=%v", threadID, pending)
	}
}

func codexRuntimeStatusFixture(t *testing.T) (*Handler, *fakeCodexLiveAgent, codexSessionCommandRuntime) {
	t.Helper()
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(t.TempDir())
	workspace := t.TempDir()
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-1"})
	bindingKey := codexBindingKey("route-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-1")
	h.ensureCodexSessions().setActiveWorkspace(bindingKey, workspace)
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
