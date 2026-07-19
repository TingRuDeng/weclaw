package messaging

import (
	"context"
	"sync"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestClaudeSelectionConcurrencyAllowsBothFrontendBindings(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		h, _, workspace := newClaudeACPNavigationHandler(t)
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
		for err := range results {
			if err != nil {
				t.Fatalf("iteration=%d acquire=%v", iteration, err)
			}
		}
		for _, routeID := range []string{"route-a", "route-b"} {
			binding := h.ensureClaudeSessions().binding(claudeBindingKey(routeID, "claude"))
			if binding.SessionID != "session-shared" || binding.Status != claudeBindingReady {
				t.Fatalf("iteration=%d route=%s binding=%+v", iteration, routeID, binding)
			}
		}
	}
}

func TestClaudeSameSessionWriterLeaseRejectsOtherFrontend(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	workspace := h.claudeWorkspaceRoot("claude")
	seedClaudeBinding(t, h, "route-a", "claude", workspace, "session-shared", 1)
	seedClaudeBinding(t, h, "route-b", "claude", workspace, "session-shared", 1)
	replyA := platformtest.NewReplier(platform.Capabilities{Text: true})
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.startAgentTask(claudeTestTaskOptions("actor-a", "route-a", replyA, ag, "first"))
	waitForAgentEnter(t, ag)
	h.startAgentTask(claudeTestTaskOptions("actor-b", "route-b", replyB, ag, "second"))
	if !containsText(replyB.Texts, "另一个窗口") {
		t.Fatalf("reply=%#v, want foreign writer rejection", replyB.Texts)
	}
	if started, _ := ag.stats(); started != 1 {
		t.Fatalf("started=%d, want one prompt", started)
	}
	ag.release <- struct{}{}
	waitUntil(t, func() bool { return h.ActiveTaskCount() == 0 })
}

func TestClaudeDifferentSessionsCanRunConcurrently(t *testing.T) {
	h, ag := newClaudeAgentTaskFixture()
	workspace := h.claudeWorkspaceRoot("claude")
	seedClaudeBinding(t, h, "route-a", "claude", workspace, "session-a", 1)
	seedClaudeBinding(t, h, "route-b", "claude", workspace, "session-b", 1)
	replyA := platformtest.NewReplier(platform.Capabilities{Text: true})
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.startAgentTask(claudeTestTaskOptions("actor-a", "route-a", replyA, ag, "first"))
	h.startAgentTask(claudeTestTaskOptions("actor-b", "route-b", replyB, ag, "second"))
	waitForAgentEnter(t, ag)
	waitForAgentEnter(t, ag)
	started, maxActive := ag.stats()
	if started != 2 || maxActive != 2 {
		t.Fatalf("started=%d maxActive=%d, want two concurrent sessions", started, maxActive)
	}
	ag.release <- struct{}{}
	ag.release <- struct{}{}
	waitUntil(t, func() bool { return h.ActiveTaskCount() == 0 })
}

func claudeTestTaskOptions(actor string, route string, reply platform.Replier, ag agent.Agent, message string) agentTaskOptions {
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeOff
	return agentTaskOptions{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: actor, routeUserID: route, reply: reply,
		agentName: "claude", message: message, agent: ag, progressCfg: cfg,
	}
}
