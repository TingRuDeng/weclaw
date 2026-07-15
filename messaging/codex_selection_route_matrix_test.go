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

type feishuOriginalRouteFixture struct {
	h                *Handler
	ag               *fakeCodexLiveAgent
	actor            string
	route            string
	actorBinding     string
	routeBinding     string
	privateWorkspace string
	targetWorkspace  string
}

// TestFeishuSessionButtonAcquiresOriginalRouteOnly 验证卡片回调只修改卡片原会话路由。
func TestFeishuSessionButtonAcquiresOriginalRouteOnly(t *testing.T) {
	fixture := newFeishuOriginalRouteFixture(t)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	fixture.h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "fs-bot", UserID: fixture.actor,
		MessageID: "session-button", Metadata: map[string]string{feishuSessionMetadataKey: fixture.route},
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": "/cx switch thread-target"}},
	}, reply)
	assertFeishuOriginalRouteAcquired(t, fixture, reply)
}

func newFeishuOriginalRouteFixture(t *testing.T) feishuOriginalRouteFixture {
	t.Helper()
	h := NewHandler(nil, nil)
	root := t.TempDir()
	privateWorkspace := filepath.Join(root, "private")
	routeWorkspace := filepath.Join(root, "route-old")
	targetWorkspace := filepath.Join(root, "target")
	h.SetAllowedWorkspaceRoots([]string{root})
	h.SetAgentWorkDirs(map[string]string{"codex": routeWorkspace})
	h.SetCodexLocalSessionDir(t.TempDir())
	h.defaultName = "codex"
	actor, route := "ou-actor", "feishu:tenant:group:chat:original"
	actorBinding, routeBinding := codexBindingKey(actor, "codex"), codexBindingKey(route, "codex")
	h.codexSessions.setThread(actorBinding, privateWorkspace, "thread-private")
	h.codexSessions.setActiveWorkspace(actorBinding, privateWorkspace)
	h.codexSessions.setThread(routeBinding, routeWorkspace, "thread-route")
	h.codexSessions.setThread(routeBinding, targetWorkspace, "thread-target")
	h.codexSessions.setActiveWorkspace(routeBinding, routeWorkspace)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{routeUserID: actor, agentName: "codex", bindingKey: actorBinding, workspace: privateWorkspace, threadID: "thread-private"})
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{routeUserID: route, agentName: "codex", bindingKey: routeBinding, workspace: routeWorkspace, threadID: "thread-route"})
	claimDesktopControlForAcquireTest(t, h, "thread-target")
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{})
	ag.setThreadBinding("thread-private", desktopAcquireBinding("thread-private"))
	ag.setThreadBinding("thread-route", desktopAcquireBinding("thread-route"))
	ag.setThreadBinding("thread-target", desktopAcquireBinding("thread-target"))
	h.agents["codex"] = ag
	return feishuOriginalRouteFixture{
		h: h, ag: ag, actor: actor, route: route,
		actorBinding: actorBinding, routeBinding: routeBinding,
		privateWorkspace: privateWorkspace, targetWorkspace: targetWorkspace,
	}
}

func assertFeishuOriginalRouteAcquired(t *testing.T, fixture feishuOriginalRouteFixture, reply *platformtest.Replier) {
	t.Helper()
	privateIntent := fixture.h.codexSessions.controlIntent("thread-private")
	routeOldIntent := fixture.h.codexSessions.controlIntent("thread-route")
	targetIntent := fixture.h.codexSessions.controlIntent("thread-target")
	privateActive, _ := fixture.h.codexSessions.getActiveWorkspace(fixture.actorBinding)
	routeActive, _ := fixture.h.codexSessions.getActiveWorkspace(fixture.routeBinding)
	if privateActive != fixture.privateWorkspace || privateIntent.Owner != codexControlRemote || privateIntent.RouteBindingKey != fixture.actorBinding {
		t.Fatalf("actor 私聊状态被误写：active=%q intent=%#v", privateActive, privateIntent)
	}
	if routeActive != fixture.targetWorkspace || routeOldIntent.Owner != codexControlDesktop ||
		targetIntent.Owner != codexControlRemote || targetIntent.RouteBindingKey != fixture.routeBinding {
		t.Fatalf("原 route 未完成接管：active=%q old=%#v target=%#v", routeActive, routeOldIntent, targetIntent)
	}
	text := strings.Join(reply.Texts, "\n")
	if !strings.Contains(text, "已切换并接管") || strings.Contains(text, fixture.route) || strings.Contains(text, fixture.actorBinding) {
		t.Fatalf("reply=%q", text)
	}
	if _, active := fixture.h.activeTask(targetIntent.ConversationID); active {
		t.Fatal("inactive 目标不应创建观察任务")
	}
	requests := fixture.ag.handoffRequests()
	if len(requests) < 1 || requests[0].Ref.ThreadID != "thread-target" || requests[0].Intent.RouteKey != fixture.routeBinding {
		t.Fatalf("handoff=%#v", requests)
	}
}

// TestCodexReadOnlyCommandsDoNotChangeOwnership 验证只读命令不修改选择、控制权或任务状态。
func TestCodexReadOnlyCommandsDoNotChangeOwnership(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := filepath.Join(t.TempDir(), "workspace")
	h.SetAllowedWorkspaceRoots([]string{workspace})
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetCodexLocalSessionDir(t.TempDir())
	h.defaultName = "codex"
	route := "readonly-route"
	bindingKey := codexBindingKey(route, "codex")
	h.codexSessions.setThread(bindingKey, workspace, "thread-readonly")
	h.codexSessions.setActiveWorkspace(bindingKey, workspace)
	claimRemoteControlForTest(t, h, fakeRemoteControlOptions{routeUserID: route, agentName: "codex", bindingKey: bindingKey, workspace: workspace, threadID: "thread-readonly"})
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeDesktop, agent.CodexThreadState{ThreadID: "thread-readonly"})
	ag.setThreadBinding("thread-readonly", desktopAcquireBinding("thread-readonly"))
	h.agents["codex"] = ag
	want := h.codexSessions.remoteSelectionSnapshot(bindingKey, "thread-readonly")

	commands := []struct {
		command string
		marker  string
	}{
		{command: "/cx ls", marker: "Codex 工作空间"},
		{command: "/cx pwd", marker: "浏览层级: 工作空间"},
		{command: "/cx status", marker: "Codex 状态:"},
		{command: "/cx whoami", marker: "workspace:"},
	}
	for index, command := range commands {
		reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
		h.HandleMessage(context.Background(), platform.IncomingMessage{
			Platform: platform.PlatformWeChat, AccountID: "wx-readonly", UserID: route,
			MessageID: "readonly-" + string(rune('0'+index)), Text: command.command,
		}, reply)
		if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], command.marker) {
			t.Fatalf("%s replies=%#v，want 单条包含 %q", command.command, reply.Texts, command.marker)
		}
		got := h.codexSessions.remoteSelectionSnapshot(bindingKey, "thread-readonly")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s 修改了选择或控制权：got=%#v want=%#v", command.command, got, want)
		}
		if len(ag.handoffRequests()) != 0 || ag.runCalls != 0 {
			t.Fatalf("%s 触发写入：handoff=%d run=%d", command.command, len(ag.handoffRequests()), ag.runCalls)
		}
		if countActiveTasks(h) != 0 {
			t.Fatalf("%s 创建了全局 observer/task", command.command)
		}
	}
}
