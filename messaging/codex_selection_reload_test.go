package messaging

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

// TestCodexRemoteSelectionReloadKeepsSingleRouteOwner 验证 A→B 提交后的全部状态可从同一文件恢复。
func TestCodexRemoteSelectionReloadKeepsSingleRouteOwner(t *testing.T) {
	h := NewHandler(nil, nil)
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	h.codexSessions.SetFilePath(stateFile)
	root := t.TempDir()
	workspaceA, workspaceB := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	h.SetAllowedWorkspaceRoots([]string{root})
	h.SetAgentWorkDirs(map[string]string{"codex": workspaceA})
	h.defaultName = "codex"
	route := "reload-route"
	bindingKey := codexBindingKey(route, "codex")
	h.codexSessions.setThread(bindingKey, workspaceA, "thread-a")
	h.codexSessions.setThread(bindingKey, workspaceB, "thread-b")
	h.codexSessions.setActiveWorkspace(bindingKey, workspaceA)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{routeUserID: route, agentName: "codex", bindingKey: bindingKey, workspace: workspaceA, threadID: "thread-a"})
	claimDesktopControlForAcquireTest(t, h, "thread-b")
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.setThreadBinding("thread-a", agent.CodexThreadBinding{Runtime: agent.CodexRuntimeWeClaw, State: agent.CodexThreadState{ThreadID: "thread-a"}})
	ag.setThreadBinding("thread-b", desktopAcquireBinding("thread-b"))
	h.agents["codex"] = ag
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformWeChat, UserID: route,
		MessageID: "reload-switch", Text: "/cx switch thread-b",
	}, reply)
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "已切换并接管") {
		t.Fatalf("reply=%#v", reply.Texts)
	}
	wantBinding := h.codexSessions.remoteSelectionSnapshot(bindingKey, "thread-b").Binding
	wantA, wantB := h.codexSessions.controlIntent("thread-a"), h.codexSessions.controlIntent("thread-b")

	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(stateFile)
	got := reloaded.remoteSelectionSnapshot(bindingKey, "thread-b")
	if !reflect.DeepEqual(got.Binding, wantBinding) {
		t.Fatalf("binding=%#v，want %#v", got.Binding, wantBinding)
	}
	if got.Binding.ActiveWorkspace != workspaceB || got.Binding.Workspaces[workspaceB].ThreadID != "thread-b" ||
		got.Binding.Workspaces[workspaceA].ThreadID != "thread-a" {
		t.Fatalf("恢复选择错误：binding=%#v", got.Binding)
	}
	gotA, gotB := reloaded.controlIntent("thread-a"), reloaded.controlIntent("thread-b")
	if !reflect.DeepEqual(gotA, wantA) || !reflect.DeepEqual(gotB, wantB) ||
		gotA.Owner != codexControlDesktop || gotB.Owner != codexControlRemote || gotB.RouteBindingKey != bindingKey {
		t.Fatalf("恢复控制权错误：A=%#v/%#v B=%#v/%#v", gotA, wantA, gotB, wantB)
	}
	if countRemoteCodexOwners(reloaded) != 1 {
		t.Fatalf("重启后出现多个 remote owner：controls=%#v", reloaded.controls)
	}
}
