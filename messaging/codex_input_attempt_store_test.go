package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestCodexInputAttemptStorePersistsMetadataWithoutExecutableInput(t *testing.T) {
	h := NewHandler(nil, nil)
	sessionFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	h.SetCodexSessionFile(sessionFile)
	secretInput := "这段完整输入绝不能持久化并在重启后自动执行"
	attempt := agent.CodexInputAttempt{
		AttemptID: "attempt-1", MessageKey: "feishu-message-1",
		ThreadID: "thread-1", BaselineTurnID: "turn-old", ExpectedTurnID: "turn-live",
		MessageDigest: "sha256:metadata-only", Status: agent.CodexInputAttemptPending,
	}

	if err := h.ensureCodexInputAttempts().record(attempt); err != nil {
		t.Fatal(err)
	}
	if err := h.ensureCodexInputAttempts().record(agent.CodexInputAttempt{
		AttemptID: attempt.AttemptID, MessageKey: attempt.MessageKey,
		ThreadID: attempt.ThreadID, BaselineTurnID: attempt.BaselineTurnID,
		ExpectedTurnID: attempt.ExpectedTurnID, MessageDigest: attempt.MessageDigest,
		Status: agent.CodexInputAttemptUnconfirmed,
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(codexInputAttemptFileForSession(sessionFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"attempt-1", "feishu-message-1", "thread-1", "turn-old", "turn-live", "sha256:metadata-only", "unconfirmed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("state=%s, want metadata %q", text, want)
		}
	}
	if strings.Contains(text, secretInput) || strings.Contains(text, "message\"") || strings.Contains(text, "input\"") {
		t.Fatalf("state persisted executable input: %s", text)
	}
	if got := len(h.ensureCodexInputAttempts().snapshot()); got != 1 {
		t.Fatalf("records=%d, want update in place", got)
	}
}
