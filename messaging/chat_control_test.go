package messaging

import (
	"context"
	"strings"
	"testing"

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
	if !strings.Contains(got, "/stop") || strings.Contains(got, "/cancel 停止当前任务") {
		t.Fatalf("expected /stop guidance, got %q", got)
	}
	if other := h.handleListActiveTasks("wechat:u2"); !strings.Contains(other, "没有运行中的任务") {
		t.Fatalf("tasks must be scoped per owner, got %q", other)
	}
}
