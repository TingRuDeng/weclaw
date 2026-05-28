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

	ag := NewHTTPAgent(HTTPAgentConfig{Endpoint: server.URL})
	_, err := ag.Chat(context.Background(), "u1", "hello")

	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Chat() error = %v, want oversized response error", err)
	}
}
