package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestACPAgentCodexRehydratePromptUsesLocalHistory(t *testing.T) {
	ctx := context.Background()
	stateFile := filepath.Join(t.TempDir(), "acp-state.json")
	workspace := t.TempDir()

	a := NewACPAgent(ACPAgentConfig{
		Command:   "codex",
		Args:      []string{"app-server", "--listen", "stdio://"},
		Cwd:       workspace,
		StateFile: stateFile,
	})
	a.recordConversationExchange("user-1", "我们之前讨论到哪了？", "我们在讨论会话持久化与恢复。")

	a.mu.Lock()
	a.threads["user-1"] = "stale-thread"
	a.mu.Unlock()
	a.persistState()

	var retryPrompt string
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"fresh-thread"}}`), nil
		case "turn/start":
			p := params.(codexTurnStartParams)

			a.notifyMu.Lock()
			ch := a.turnCh[p.ThreadID]
			a.notifyMu.Unlock()
			if ch == nil {
				return nil, fmt.Errorf("missing turn channel for thread %s", p.ThreadID)
			}

			if p.ThreadID == "stale-thread" {
				ch <- &codexTurnEvent{Kind: "completed"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			if p.ThreadID == "fresh-thread" {
				retryPrompt = p.Input[0].Text
				ch <- &codexTurnEvent{Delta: "ok"}
				ch <- &codexTurnEvent{Kind: "completed"}
				return json.RawMessage(`{"ok":true}`), nil
			}
			return nil, fmt.Errorf("unexpected thread id: %s", p.ThreadID)
		default:
			return nil, fmt.Errorf("unexpected method: %s", method)
		}
	}

	reply, err := a.chatCodexAppServer(ctx, "user-1", "继续刚才的话题", nil)
	if err != nil {
		t.Fatalf("chatCodexAppServer error: %v", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want %q", reply, "ok")
	}
	if retryPrompt == "" {
		t.Fatal("retryPrompt is empty")
	}
	if !containsAll(retryPrompt,
		"Context from the previous conversation",
		"我们之前讨论到哪了？",
		"会话持久化与恢复",
		"继续刚才的话题",
	) {
		t.Fatalf("retry prompt did not contain expected context.\nretryPrompt=%q", retryPrompt)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
