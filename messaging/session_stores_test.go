package messaging

import (
	"sync"
	"testing"
)

func TestNewHandlerOwnsSessionStoresThroughService(t *testing.T) {
	h := NewHandler(nil, nil)
	if h.sessions == nil {
		t.Fatal("session service is nil")
	}
	if h.ensureAgentSessions() != h.sessions.agent {
		t.Fatal("agent session store is not owned by session service")
	}
	if h.ensureCodexSessions() != h.sessions.codex {
		t.Fatal("codex session store is not owned by session service")
	}
	if h.ensureClaudeSessions() != h.sessions.claude {
		t.Fatal("claude session store is not owned by session service")
	}
}

func TestHandlerLazilyInitializesOneSessionService(t *testing.T) {
	h := &Handler{}
	const workers = 32
	services := make(chan *sessionService, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = h.ensureAgentSessions()
			_ = h.ensureCodexSessions()
			_ = h.ensureClaudeSessions()
			services <- h.ensureSessionService()
		}()
	}
	wait.Wait()
	close(services)

	var first *sessionService
	for service := range services {
		if first == nil {
			first = service
			continue
		}
		if service != first {
			t.Fatal("concurrent initialization created more than one session service")
		}
	}
}
