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

// TestFeishuSessionButtonBindsOriginalRouteOnly 验证卡片回调只修改卡片原会话路由。
func TestFeishuSessionButtonBindsOriginalRouteOnly(t *testing.T) {
	fixture := newFeishuOriginalRouteFixture(t)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	fixture.h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "fs-bot", UserID: fixture.actor,
		MessageID: "session-button", Metadata: map[string]string{feishuSessionMetadataKey: fixture.route},
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": "/cx switch thread-target"}},
	}, reply)
	assertFeishuOriginalRouteBound(t, fixture, reply)
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
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{})
	for _, threadID := range []string{"thread-private", "thread-route", "thread-target"} {
		ag.setThreadBinding(threadID, agent.CodexThreadBinding{
			Runtime: agent.CodexRuntimeWeClaw,
			State:   agent.CodexThreadState{ThreadID: threadID},
		})
	}
	h.agents["codex"] = ag
	return feishuOriginalRouteFixture{
		h: h, ag: ag, actor: actor, route: route,
		actorBinding: actorBinding, routeBinding: routeBinding,
		privateWorkspace: privateWorkspace, targetWorkspace: targetWorkspace,
	}
}

func assertFeishuOriginalRouteBound(t *testing.T, fixture feishuOriginalRouteFixture, reply *platformtest.Replier) {
	t.Helper()
	privateActive, _ := fixture.h.codexSessions.getActiveWorkspace(fixture.actorBinding)
	routeActive, _ := fixture.h.codexSessions.getActiveWorkspace(fixture.routeBinding)
	if privateActive != fixture.privateWorkspace {
		t.Fatalf("actor 私聊状态被误写：active=%q", privateActive)
	}
	if routeActive != fixture.targetWorkspace {
		t.Fatalf("原 route 未完成绑定：active=%q", routeActive)
	}
	text := strings.Join(reply.Texts, "\n")
	if !strings.Contains(text, "已切换并绑定") || strings.Contains(text, fixture.route) || strings.Contains(text, fixture.actorBinding) {
		t.Fatalf("reply=%q", text)
	}
	conversationID := buildCodexConversationID(fixture.route, "codex", fixture.targetWorkspace)
	if _, active := fixture.h.activeTask(conversationID); active {
		t.Fatal("inactive 目标不应创建观察任务")
	}
	requests := fixture.ag.handoffRequests()
	if len(requests) < 1 || requests[0].Ref.ThreadID != "thread-target" || requests[0].Intent.RouteKey != fixture.routeBinding {
		t.Fatalf("handoff=%#v", requests)
	}
}

// TestCodexReadOnlyCommandsDoNotChangeBinding 验证只读命令不修改选择或任务状态。
func TestCodexReadOnlyCommandsDoNotChangeBinding(t *testing.T) {
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
	ag := newFakeCodexLiveAgent(agent.CodexRuntimeWeClaw, agent.CodexThreadState{ThreadID: "thread-readonly"})
	ag.setThreadBinding("thread-readonly", agent.CodexThreadBinding{
		Runtime: agent.CodexRuntimeWeClaw,
		State:   agent.CodexThreadState{ThreadID: "thread-readonly"},
	})
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
			t.Fatalf("%s 修改了 frontend binding：got=%#v want=%#v", command.command, got, want)
		}
		if len(ag.handoffRequests()) != 0 || ag.runCalls != 0 {
			t.Fatalf("%s 触发写入：handoff=%d run=%d", command.command, len(ag.handoffRequests()), ag.runCalls)
		}
		if countActiveTasks(h) != 0 {
			t.Fatalf("%s 创建了全局 observer/task", command.command)
		}
	}
}
