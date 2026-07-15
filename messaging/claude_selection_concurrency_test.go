package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

// TestClaudeSelectionConcurrencySingleRemoteWinner 验证两个 route 并发选择同一 session 时始终只有一个赢家。
func TestClaudeSelectionConcurrencySingleRemoteWinner(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		h, _, workspace := newClaudeACPNavigationHandler(t)
		store := h.ensureClaudeSessions()
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, routeID := range []string{"route-a", "route-b"} {
			routeID := routeID
			fake := &fakeClaudeSessionAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{
				Name: "claude", Type: "acp", Command: "claude-agent-acp",
			}}}
			route := newClaudeAcquireRoute(context.Background(), routeID, routeID, "claude", fake, workspace)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := acquireClaudeSessionForTest(h, claudeSessionAcquireRequest{
					Route: route, Selected: agent.ClaudeSession{ID: "session-shared", Cwd: workspace}, Command: "switch",
				})
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		successes := 0
		conflicts := 0
		for err := range results {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, errClaudeRemoteSelectionOtherRoute), errors.Is(err, errClaudeRemoteSelectionChanged):
				conflicts++
			default:
				t.Fatalf("iteration=%d unexpected error=%v", iteration, err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("iteration=%d successes=%d conflicts=%d", iteration, successes, conflicts)
		}
		intent := store.controlIntent("session-shared")
		if intent.Owner != claudeOwnerRemote || intent.BindingKey == "" || intent.Revision != 1 {
			t.Fatalf("iteration=%d intent=%+v", iteration, intent)
		}
		remoteOwners := 0
		store.mu.Lock()
		for _, control := range store.controls {
			if control.Owner == claudeOwnerRemote {
				remoteOwners++
			}
		}
		store.mu.Unlock()
		if remoteOwners != 1 {
			t.Fatalf("iteration=%d remoteOwners=%d", iteration, remoteOwners)
		}
	}
}
