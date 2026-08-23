package messaging

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

type stoppableFakeAgent struct {
	fakeAgent
	stops atomic.Int32
}

func (a *stoppableFakeAgent) Stop() { a.stops.Add(1) }

func TestStopAgentsStopsProcessOwningAgents(t *testing.T) {
	h := newTestHandler()
	first := &stoppableFakeAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "first"}}}
	second := &stoppableFakeAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "second"}}}
	h.agents["first"] = first
	h.agents["second"] = second
	h.agents["http"] = &fakeAgent{}

	h.StopAgents()

	if first.stops.Load() != 1 || second.stops.Load() != 1 {
		t.Fatalf("stops=(%d,%d), want each process-owning agent stopped once", first.stops.Load(), second.stops.Load())
	}
}

func TestEnsureAgentStartedStopsLateAgentAfterShutdownBegins(t *testing.T) {
	created := &stoppableFakeAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "late"}}}
	h := NewHandlerWithErrorFactory(func(context.Context, string) (agent.Agent, error) {
		return created, nil
	}, nil)
	h.StopAgents()
	if _, err := h.EnsureAgentStarted(context.Background(), "late"); err == nil {
		t.Fatal("late Agent startup must fail after shutdown begins")
	}
	if created.stops.Load() != 1 {
		t.Fatalf("late Agent stops=%d, want 1", created.stops.Load())
	}
}

func (a *stoppableFakeAgent) Chat(ctx context.Context, conversationID, message string) (string, error) {
	return a.fakeAgent.Chat(ctx, conversationID, message)
}
