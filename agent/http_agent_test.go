package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
