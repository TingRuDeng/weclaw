package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

// TestCodexOwnerCommandsAreRecognizedAsSessionCommands 防止控制权命令再次绕过内置命令入口。
func TestCodexOwnerCommandsAreRecognizedAsSessionCommands(t *testing.T) {
	commands := []string{"/cx owner remote", "/cx owner desktop"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if !isCodexSessionCommand(command) {
				t.Fatalf("%q 未被识别为 Codex 会话命令", command)
			}
		})
	}
}

// TestHandleMessageRoutesCodexOwnerRemoteToOwnerCommand 验证真实消息入口会执行控制权移交而不是启动普通任务。
func TestHandleMessageRoutesCodexOwnerRemoteToOwnerCommand(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	h.SetDefaultAgent("codex", ag)
	h.SetAgentWorkDirs(map[string]string{"codex": runtime.workspaceRoot})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformWeChat,
		UserID:   "route-1",
		Text:     "/cx owner remote",
	}, reply)

	intent := h.codexSessions.controlIntent("thread-1")
	if intent.Owner != codexControlRemote || ag.handoffCalls != 1 {
		t.Fatalf("intent=%#v handoff=%d，owner 命令未进入控制权处理链", intent, ag.handoffCalls)
	}
	if text := strings.Join(reply.Texts, "\n"); strings.Contains(text, "尚未选择控制方") {
		t.Fatalf("owner 命令被错误当作普通任务处理: %q", text)
	}
}
