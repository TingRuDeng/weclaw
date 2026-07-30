package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAgentRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "9000000")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	ag, err := NewHTTPAgent(HTTPAgentConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatalf("NewHTTPAgent error: %v", err)
	}
	_, err = ag.Chat(context.Background(), "u1", "hello")

	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Chat() error = %v, want oversized response error", err)
	}
}

func TestNewHTTPAgentRejectsNegativeMaxHistory(t *testing.T) {
	if _, err := NewHTTPAgent(HTTPAgentConfig{Endpoint: "https://example.com", MaxHistory: -1}); err == nil {
		t.Fatal("NewHTTPAgent error=nil, want negative max_history rejection")
	}
}

func TestHTTPAgentEvictsLeastRecentlyUsedConversationAtCapacity(t *testing.T) {
	server := newHTTPAgentSuccessServer(t)
	defer server.Close()
	ag, err := NewHTTPAgent(HTTPAgentConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	ag.now = func() time.Time { return now }
	ag.maxConversations = 2

	for _, conversationID := range []string{"conversation-1", "conversation-2", "conversation-3"} {
		if _, err := ag.Chat(context.Background(), conversationID, "hello"); err != nil {
			t.Fatalf("Chat(%q): %v", conversationID, err)
		}
		now = now.Add(time.Minute)
	}

	ag.mu.Lock()
	defer ag.mu.Unlock()
	if len(ag.history) != 2 {
		t.Fatalf("history size=%d, want 2", len(ag.history))
	}
	if _, ok := ag.history["conversation-1"]; ok {
		t.Fatal("least recently used conversation was not evicted")
	}
}

func TestHTTPAgentExpiresIdleConversationHistory(t *testing.T) {
	server := newHTTPAgentSuccessServer(t)
	defer server.Close()
	ag, err := NewHTTPAgent(HTTPAgentConfig{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	ag.now = func() time.Time { return now }
	ag.historyTTL = time.Hour

	if _, err := ag.Chat(context.Background(), "stale", "hello"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := ag.Chat(context.Background(), "current", "hello"); err != nil {
		t.Fatal(err)
	}

	ag.mu.Lock()
	defer ag.mu.Unlock()
	if _, ok := ag.history["stale"]; ok {
		t.Fatal("idle conversation history was not expired")
	}
	if _, ok := ag.history["current"]; !ok {
		t.Fatal("current conversation history is missing")
	}
}

func newHTTPAgentSuccessServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
}
