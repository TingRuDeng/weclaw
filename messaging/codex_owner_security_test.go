package messaging

import (
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

const codexOwnerInternalErrorText = "/private/run/codex.sock route=route-secret conversation=conversation-secret"

func TestCodexOwnerRemoteFailureDoesNotLeakInternalDetails(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.handoffErrors["thread-1"] = errors.New(codexOwnerInternalErrorText)
	runtime.fields = []string{"/cx", "owner", "remote"}

	result := h.handleCodexOwnerCommand(runtime)

	assertCodexOwnerReplySafe(t, result.Reply)
	if !strings.Contains(result.Reply, "所有权已保留") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func TestCodexOwnerDesktopFailureDoesNotLeakInternalDetails(t *testing.T) {
	h, ag, runtime := codexRemoteOwnerCommandFixture(t)
	ag.handoffErrors["thread-1"] = errors.New(codexOwnerInternalErrorText)
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	assertCodexOwnerReplySafe(t, result.Reply)
	if !strings.Contains(result.Reply, "已归还") || !strings.Contains(result.Reply, "远程写入已关闭") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func TestCodexOwnerPersistenceFailureDoesNotLeakInternalDetails(t *testing.T) {
	h, _, runtime := codexRemoteOwnerCommandFixture(t)
	h.codexSessions.SetFilePath(t.TempDir() + "/state.json")
	h.codexSessions.writeState = func(string, []byte) error {
		return errors.New(codexOwnerInternalErrorText)
	}
	runtime.fields = []string{"/cx", "owner", "desktop"}

	result := h.handleCodexOwnerCommand(runtime)

	assertCodexOwnerReplySafe(t, result.Reply)
	if !strings.Contains(result.Reply, "控制权提交失败") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func TestCodexOwnerStatusFailureDoesNotLeakInternalDetails(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.bindErr = errors.New(codexOwnerInternalErrorText)

	result := h.handleCodexOwnerCommand(runtime)

	assertCodexOwnerReplySafe(t, result.Reply)
	if !strings.Contains(result.Reply, "查询 Codex 控制权失败") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func TestCodexOwnerStatusProbeWarningDoesNotLeakInternalDetails(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.bindErr = errors.Join(agent.ErrCodexDesktopOwnershipUnknown, errors.New(codexOwnerInternalErrorText))

	result := h.handleCodexOwnerCommand(runtime)

	assertCodexOwnerReplySafe(t, result.Reply)
	if !strings.Contains(result.Reply, "探测: Desktop 控制权未确认") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func TestCodexOwnerStatusConflictDoesNotLeakInternalDetails(t *testing.T) {
	h, ag, runtime := codexOwnerCommandFixture(t)
	ag.mu.Lock()
	ag.binding.Runtime = agent.CodexRuntimeConflict
	ag.binding.ConflictReason = codexOwnerInternalErrorText
	ag.mu.Unlock()

	result := h.handleCodexOwnerCommand(runtime)

	assertCodexOwnerReplySafe(t, result.Reply)
	if !strings.Contains(result.Reply, "冲突: 运行时写入冲突") {
		t.Fatalf("reply=%q", result.Reply)
	}
}

func assertCodexOwnerReplySafe(t *testing.T, reply string) {
	t.Helper()
	for _, forbidden := range []string{"/private/", "codex.sock", "route-secret", "conversation-secret"} {
		if strings.Contains(reply, forbidden) {
			t.Fatalf("reply=%q 泄露内部文本 %q", reply, forbidden)
		}
	}
}
