package agent

import (
	"testing"
	"time"
)

func TestCodexThreadActivityHandlerFiresWithoutTurnOwner(t *testing.T) {
	a := newACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}}, acpAgentOptions{})
	activity := make(chan string, 1)
	a.SetCodexThreadActivityHandler(func(threadID string) {
		activity <- threadID
	})

	if delivered := a.dispatchToTurnCh("thread-local", &codexTurnEvent{
		Kind: "started", TurnID: "turn-local-1",
	}); delivered {
		t.Fatal("event unexpectedly had a turn owner")
	}
	select {
	case threadID := <-activity:
		if threadID != "thread-local" {
			t.Fatalf("activity thread=%q", threadID)
		}
	case <-time.After(time.Second):
		t.Fatal("thread activity did not wake the frontend follower")
	}
}
