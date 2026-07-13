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

func TestModeCommandTogglesYolo(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"

	if got := h.handleModeCommand(user, "/mode"); !strings.Contains(got, "default") {
		t.Fatalf("default mode expected, got %q", got)
	}
	if got := h.handleModeCommand(user, "/mode yolo"); !strings.Contains(got, "yolo") {
		t.Fatalf("switch to yolo expected, got %q", got)
	}
	if !h.isYoloMode(user) {
		t.Fatal("expected yolo mode active")
	}
	if got := h.handleModeCommand(user, "/mode default"); !strings.Contains(got, "default") {
		t.Fatalf("switch to default expected, got %q", got)
	}
	if h.isYoloMode(user) {
		t.Fatal("expected yolo mode cleared")
	}
	if got := h.handleModeCommand(user, "/mode bogus"); !strings.Contains(got, "用法") {
		t.Fatalf("usage hint expected for unknown subcommand, got %q", got)
	}
}

func TestModeYoloIsolatedPerUser(t *testing.T) {
	h := NewHandler(nil, nil)
	h.setYoloMode("wechat:u1", true)
	if h.isYoloMode("wechat:u2") {
		t.Fatal("yolo must not leak across users")
	}
}

// TestModeYoloIsolatedPerFeishuSession 验证同一用户的不同飞书窗口互不共享审批模式。
func TestModeYoloIsolatedPerFeishuSession(t *testing.T) {
	h := NewHandler(nil, nil)
	routeA := "feishu:tenant:dm:chat-a:ou_user"
	routeB := "feishu:tenant:group:chat-b"
	h.HandleMessage(context.Background(), modeCommandMessage("mode-a", routeA, "/mode yolo"), platformtest.NewReplier(platform.Capabilities{Text: true}))
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), modeCommandMessage("mode-b", routeB, "/mode"), replyB)

	if !h.isYoloMode(routeA) || h.isYoloMode(routeB) {
		t.Fatalf("routeA=%t routeB=%t，期望审批模式按飞书窗口隔离", h.isYoloMode(routeA), h.isYoloMode(routeB))
	}
	if !containsText(replyB.Texts, "default") {
		t.Fatalf("窗口 B 回复=%#v，期望保持 default", replyB.Texts)
	}
	if !strings.Contains(h.buildStatusForRoute("ou_user", routeA, platform.PlatformFeishu, "cli_main"), "mode: yolo") {
		t.Fatal("窗口 A 状态应显示 yolo")
	}
	if !strings.Contains(h.buildStatusForRoute("ou_user", routeB, platform.PlatformFeishu, "cli_main"), "mode: default") {
		t.Fatal("窗口 B 状态应显示 default")
	}
}

// modeCommandMessage 构造指定飞书窗口的审批模式命令。
func modeCommandMessage(messageID string, route string, text string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: messageID, Text: text,
		Metadata: map[string]string{feishuSessionMetadataKey: route},
	}
}

func TestApprovalHandlerYoloAutoApproves(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"
	h.setYoloMode(user, true)

	denyReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	handler := h.approvalHandlerForUser(user, user, denyReply)
	options := []agent.ApprovalOption{
		{ID: "deny-1", Kind: "deny"},
		{ID: "allow-1", Kind: "allow"},
	}
	decision, err := handler(context.Background(), agent.ApprovalRequest{Options: options})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "allow-1" {
		t.Fatalf("expected allow option auto-approved, got %q", decision)
	}
	if len(denyReply.Choices) != 0 {
		t.Fatalf("yolo mode must not prompt buttons, got %d AskChoices calls", len(denyReply.Choices))
	}
}

// TestApprovalHandlerReadsRouteMode 验证审批只读取任务所属窗口的模式。
func TestApprovalHandlerReadsRouteMode(t *testing.T) {
	h := NewHandler(nil, nil)
	routeA := "feishu:tenant:dm:chat-a:ou_user"
	routeB := "feishu:tenant:group:chat-b"
	h.setYoloMode(routeA, true)
	options := []agent.ApprovalOption{{ID: "deny-1", Kind: "deny"}, {ID: "allow-1", Kind: "allow"}}

	replyA := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctxA, cancelA := context.WithTimeout(context.Background(), taskQueueProbeDelay)
	defer cancelA()
	decision, err := h.approvalHandlerForUser("ou_user", routeA, replyA)(ctxA, agent.ApprovalRequest{Options: options})
	if err != nil || decision != "allow-1" || len(replyA.Choices) != 0 {
		t.Fatalf("窗口 A decision=%q err=%v choices=%#v，期望自动同意", decision, err, replyA.Choices)
	}
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.approvalHandlerForUser("ou_user", routeB, replyB)(ctx, agent.ApprovalRequest{Options: options})
	if len(replyB.Choices) != 1 {
		t.Fatalf("窗口 B choices=%#v，期望继续按钮确认", replyB.Choices)
	}
}

func TestApprovalHandlerYoloAutoApprovesCodexFileChangeDecision(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"
	h.setYoloMode(user, true)

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	handler := h.approvalHandlerForUser(user, user, reply)
	options := []agent.ApprovalOption{
		{ID: "accept", Kind: "allow"},
		{ID: "cancel", Kind: "deny"},
	}
	decision, err := handler(context.Background(), agent.ApprovalRequest{Options: options})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "accept" {
		t.Fatalf("expected accept decision auto-approved, got %q", decision)
	}
	if len(reply.Choices) != 0 {
		t.Fatalf("yolo mode must not prompt buttons, got %d AskChoices calls", len(reply.Choices))
	}
}

func TestListActiveTasksEmptyAndPopulated(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"
	if got := h.handleListActiveTasks(user); !strings.Contains(got, "没有运行中的任务") {
		t.Fatalf("expected empty message, got %q", got)
	}

	_, _, started := h.beginActiveTask(context.Background(), "key-1", activeTaskMeta{
		owner:     user,
		agentName: "codex",
		message:   "重构登录模块",
	})
	if !started {
		t.Fatal("expected active task to start")
	}
	got := h.handleListActiveTasks(user)
	if !strings.Contains(got, "codex") || !strings.Contains(got, "重构登录模块") {
		t.Fatalf("expected running codex task listed, got %q", got)
	}
	task, ok := h.activeTask("key-1")
	if !ok {
		t.Fatal("expected active task to exist")
	}
	task.recordProgress(time.Now(), "正在修改表单组件")
	got = h.handleListActiveTasks(user)
	if !strings.Contains(got, "最近进展") || !strings.Contains(got, "正在修改表单组件") {
		t.Fatalf("expected active task progress listed, got %q", got)
	}
	if !strings.Contains(got, "/stop") || strings.Contains(got, "/cancel 停止当前任务") {
		t.Fatalf("expected /stop guidance, got %q", got)
	}
	if other := h.handleListActiveTasks("wechat:u2"); !strings.Contains(other, "没有运行中的任务") {
		t.Fatalf("tasks must be scoped per owner, got %q", other)
	}
}

func TestRunningTasksFooterDoesNotPromptCodexAppOperation(t *testing.T) {
	readOnly := runningTasksFooter([]runningTaskView{{stoppable: false}})
	if strings.Contains(readOnly, "Codex App") || strings.Contains(readOnly, "App 中操作") {
		t.Fatalf("read-only footer must not prompt app operation, got %q", readOnly)
	}
	if !strings.Contains(readOnly, "结果会自动返回当前会话") {
		t.Fatalf("read-only footer must describe result delivery, got %q", readOnly)
	}
	mixed := runningTasksFooter([]runningTaskView{{stoppable: true}, {stoppable: false}})
	if strings.Contains(mixed, "Codex App") || strings.Contains(mixed, "App 中操作") {
		t.Fatalf("mixed footer must not prompt app operation, got %q", mixed)
	}
}
