package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

// fakeClaudeSessionCreateAgent 模拟真实 ACP：session/new 成功后会立即更新 conversation runtime。
type fakeClaudeSessionCreateAgent struct {
	*fakeClaudeSessionAgent
	resetCalls int
}

func (f *fakeClaudeSessionCreateAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	f.resetCalls++
	f.fakeAgent.mu.Lock()
	f.fakeAgent.resetConversation = conversationID
	sessionID := f.fakeAgent.resetSessionID
	f.fakeAgent.mu.Unlock()
	if sessionID != "" {
		if f.runtimeSessions == nil {
			f.runtimeSessions = make(map[string]string)
		}
		f.runtimeSessions[conversationID] = sessionID
		f.sessionID = sessionID
	}
	return sessionID, f.resetErr
}

func TestClaudeNewCreatesAndOwnsSessionAtomically(t *testing.T) {
	h, fake, workspace := newClaudeSessionCreateHandler(t)
	fake.resetSessionID = "session-new"

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	key := claudeBindingKey("user-1", "claude")
	intent := h.ensureClaudeSessions().controlIntent("session-new")
	if !strings.Contains(text, "已创建并接管") {
		t.Fatalf("text=%q", text)
	}
	if intent.Owner != claudeOwnerRemote || intent.BindingKey != key {
		t.Fatalf("intent=%+v", intent)
	}
	if binding := h.ensureClaudeSessions().binding(key); binding.WorkspaceRoot != workspace || binding.SessionID != "session-new" {
		t.Fatalf("binding=%+v", binding)
	}
	if fake.resetCalls != 1 || len(fake.useCalls) != 0 {
		t.Fatalf("resetCalls=%d useCalls=%v，session/new 后不得额外 resume", fake.resetCalls, fake.useCalls)
	}
}

func TestClaudeNewRejectsEmptySessionIDWithoutOwner(t *testing.T) {
	h, fake, _ := newClaudeSessionCreateHandler(t)
	fake.resetSessionID = ""

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	if !strings.Contains(text, "新建 Claude 会话失败") || strings.Contains(text, "sessionId") || fake.resetCalls != 1 {
		t.Fatalf("text=%q resetCalls=%d", text, fake.resetCalls)
	}
	store := h.ensureClaudeSessions()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.controls) != 0 {
		t.Fatalf("controls=%+v", store.controls)
	}
}

func TestClaudeNewStoreFailureRestoresTrueRuntimeAndOwner(t *testing.T) {
	h, fake, workspace := newClaudeSessionCreateHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-old", claudeBindingReady)
	store.controls["session-old"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	fake.runtimeSessions = map[string]string{conversationID: "session-old"}
	fake.sessionID = "session-old"
	fake.resetSessionID = "session-new"
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	if !strings.Contains(text, "失败") || store.binding(key).SessionID != "session-old" {
		t.Fatalf("text=%q binding=%+v", text, store.binding(key))
	}
	if got := store.controlIntent("session-old"); got.Owner != claudeOwnerRemote || got.BindingKey != key {
		t.Fatalf("old intent=%+v", got)
	}
	if got := store.controlIntent("session-new"); got.Owner == claudeOwnerRemote {
		t.Fatalf("orphan intent=%+v", got)
	}
	if current, ok := fake.CurrentClaudeSession(conversationID); !ok || current != "session-old" {
		t.Fatalf("runtime=(%q,%t)", current, ok)
	}
	if fake.resetCalls != 1 || len(fake.useCalls) != 1 || fake.useCalls[0] != "session-old" {
		t.Fatalf("resetCalls=%d useCalls=%v", fake.resetCalls, fake.useCalls)
	}
}

func TestClaudeNewAgentSelectionFailureRestoresTrueRuntimeAndOwner(t *testing.T) {
	h, fake, workspace := newClaudeSessionCreateHandler(t)
	key := claudeBindingKey("user-1", "claude")
	conversationID := buildClaudeConversationID("user-1", "claude", workspace)
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-old", claudeBindingReady)
	store.controls["session-old"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key, ConversationID: conversationID, Revision: 1,
	}
	fake.runtimeSessions = map[string]string{conversationID: "session-old"}
	fake.sessionID = "session-old"
	fake.resetSessionID = "session-new"
	statePath := filepath.Join(t.TempDir(), "agent-sessions.json")
	if err := h.SetAgentSessionFile(statePath); err != nil {
		t.Fatal(err)
	}
	if err := h.ensureAgentSessions().Set("user-1", "codex"); err != nil {
		t.Fatal(err)
	}
	invalidParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.ensureAgentSessions().filePath = filepath.Join(invalidParent, "state.json")

	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	selected, _ := h.ensureAgentSessions().Get("user-1")
	if !strings.Contains(text, "失败") || selected != "codex" || store.binding(key).SessionID != "session-old" {
		t.Fatalf("text=%q selected=%q binding=%+v", text, selected, store.binding(key))
	}
	if got := store.controlIntent("session-old"); got.Owner != claudeOwnerRemote || got.BindingKey != key {
		t.Fatalf("old intent=%+v", got)
	}
	if got := store.controlIntent("session-new"); got.Owner == claudeOwnerRemote {
		t.Fatalf("orphan intent=%+v", got)
	}
	if current, ok := fake.CurrentClaudeSession(conversationID); !ok || current != "session-old" {
		t.Fatalf("runtime=(%q,%t)", current, ok)
	}
}

func newClaudeSessionCreateHandler(t *testing.T) (*Handler, *fakeClaudeSessionCreateAgent, string) {
	t.Helper()
	workspace := t.TempDir()
	base := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{
		info: agent.AgentInfo{Name: "claude", Type: "acp", Command: "claude-agent-acp"},
	}}
	fake := &fakeClaudeSessionCreateAgent{fakeClaudeSessionAgent: base}
	h := NewHandler(nil, nil)
	h.defaultName = "claude"
	h.agents["claude"] = fake
	h.SetAgentWorkDirs(map[string]string{"claude": workspace})
	h.SetAllowedWorkspaceRoots([]string{workspace})
	return h, fake, workspace
}
