package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestClaudeSelectionRouteMatrixWriteEntries(t *testing.T) {
	t.Run("switch changes only frontend binding", func(t *testing.T) {
		h, fake, workspace := newClaudeACPNavigationHandler(t)
		key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-old", 4)
		other := seedClaudeBinding(t, h, "user-2", "claude", workspace, "session-old", 2)
		fake.catalogSessions = []agent.ClaudeSession{{ID: "session-new", Cwd: workspace}}

		_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc switch session-new")
		if got := h.ensureClaudeSessions().binding(key); got.SessionID != "session-new" || got.Revision <= 4 {
			t.Fatalf("selected=%+v", got)
		}
		if got := h.ensureClaudeSessions().binding(other); got.SessionID != "session-old" {
			t.Fatalf("other route changed=%+v", got)
		}
		if len(fake.useCalls) != 1 {
			t.Fatalf("useCalls=%v", fake.useCalls)
		}
	})

	t.Run("new binds without duplicate resume", func(t *testing.T) {
		h, fake, _ := newClaudeSessionCreateHandler(t)
		fake.resetSessionID = "session-new"
		_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
		binding := h.ensureClaudeSessions().binding(claudeBindingKey("user-1", "claude"))
		if binding.SessionID != "session-new" || binding.Status != claudeBindingReady || len(fake.useCalls) != 0 {
			t.Fatalf("binding=%+v useCalls=%v", binding, fake.useCalls)
		}
	})
}

func TestClaudeSelectionRouteMatrixReadOnlyAndDisabledCommandsPreserveBinding(t *testing.T) {
	commands := []string{"/cc ls", "/cc pwd", "/cc status", "/cc owner", "/cc cli", "/cc quota", "/cc model ls"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			h, fake, workspace := newClaudeACPNavigationHandler(t)
			key := seedClaudeBinding(t, h, "user-1", "claude", workspace, "session-a", 7)
			fake.catalogSessions = []agent.ClaudeSession{{ID: "session-a", Cwd: workspace}}
			before := h.ensureClaudeSessions().binding(key)
			_ = h.handleClaudeSessionCommand(context.Background(), "user-1", command)
			if after := h.ensureClaudeSessions().binding(key); after != before {
				t.Fatalf("command=%q before=%+v after=%+v", command, before, after)
			}
		})
	}
}
