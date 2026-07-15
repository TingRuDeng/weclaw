package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestClaudeSelectionRouteMatrixWriteEntries(t *testing.T) {
	t.Run("switch acquires and releases previous", func(t *testing.T) {
		h, fake, workspace := newClaudeACPNavigationHandler(t)
		key := claudeBindingKey("user-1", "claude")
		store := h.ensureClaudeSessions()
		oldConversation := buildClaudeConversationID("user-1", "claude", workspace)
		store.bindings[key] = newClaudeBinding(workspace, "session-old", claudeBindingReady)
		store.controls["session-old"] = claudeControlIntent{
			Owner: claudeOwnerRemote, BindingKey: key, ConversationID: oldConversation, Revision: 4,
		}
		fake.runtimeSessions = map[string]string{oldConversation: "session-old"}
		fake.catalogSessions = []agent.ClaudeSession{{ID: "session-new", Cwd: workspace}}

		_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc switch session-new")
		if got := store.controlIntent("session-old"); got.Owner != claudeOwnerLocal || got.Revision != 5 {
			t.Fatalf("old=%+v", got)
		}
		if got := store.controlIntent("session-new"); got.Owner != claudeOwnerRemote || got.BindingKey != key || got.Revision != 1 {
			t.Fatalf("new=%+v", got)
		}
		if len(fake.useCalls) != 1 || store.binding(key).SessionID != "session-new" {
			t.Fatalf("useCalls=%v binding=%+v", fake.useCalls, store.binding(key))
		}
	})

	t.Run("new acquires without duplicate resume", func(t *testing.T) {
		h, fake, _ := newClaudeSessionCreateHandler(t)
		fake.resetSessionID = "session-new"
		_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
		intent := h.ensureClaudeSessions().controlIntent("session-new")
		if intent.Owner != claudeOwnerRemote || intent.Revision != 1 || len(fake.useCalls) != 0 {
			t.Fatalf("intent=%+v useCalls=%v", intent, fake.useCalls)
		}
	})

	t.Run("owner local and remote are explicit", func(t *testing.T) {
		h, fake, workspace := newClaudeACPNavigationHandler(t)
		seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-a", 3)
		fake.catalogSessions = []agent.ClaudeSession{{ID: "session-a", Cwd: workspace}}
		_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
		if got := h.ensureClaudeSessions().controlIntent("session-a"); got.Owner != claudeOwnerLocal || got.Revision != 4 {
			t.Fatalf("local=%+v", got)
		}
		_ = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner remote")
		if got := h.ensureClaudeSessions().controlIntent("session-a"); got.Owner != claudeOwnerRemote || got.Revision != 5 {
			t.Fatalf("remote=%+v", got)
		}
	})
}

func TestClaudeSelectionRouteMatrixReadOnlyCommandsPreserveControlSnapshot(t *testing.T) {
	commands := []string{"/cc ls", "/cc pwd", "/cc status", "/cc owner", "/cc model ls"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			h, fake, workspace := newClaudeACPNavigationHandler(t)
			seedClaudeRemoteControl(t, h, "user-1", "claude", workspace, "session-a", 7)
			fake.catalogSessions = []agent.ClaudeSession{{ID: "session-a", Cwd: workspace}}
			store := h.ensureClaudeSessions()
			before := newClaudeSessionStoreImage(store.bindings, store.controls)

			_ = h.handleClaudeSessionCommand(context.Background(), "user-1", command)

			store.mu.Lock()
			after := newClaudeSessionStoreImage(store.bindings, store.controls)
			store.mu.Unlock()
			if !sameClaudeSessionStoreImage(before, after) {
				t.Fatalf("command=%q before=%+v after=%+v", command, before, after)
			}
		})
	}
}
