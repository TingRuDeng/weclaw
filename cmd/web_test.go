package cmd

import (
	"errors"
	"strings"
	"testing"
)

type failingWebTokenReader struct{}

func TestWebPanelURLUsesFragmentForToken(t *testing.T) {
	got := webPanelURL("127.0.0.1:39282", "secret value")
	if strings.Contains(got, "?token=") || !strings.Contains(got, "#token=secret+value") {
		t.Fatalf("webPanelURL=%q, want token only in URL fragment", got)
	}
}

func (failingWebTokenReader) Read([]byte) (int, error) {
	return 0, errors.New("random source failed")
}

func TestGenerateWebTokenReturnsErrorWhenRandomFails(t *testing.T) {
	oldReader := webTokenRandomReader
	webTokenRandomReader = failingWebTokenReader{}
	t.Cleanup(func() {
		webTokenRandomReader = oldReader
	})

	token, err := generateWebToken()

	if err == nil {
		t.Fatal("generateWebToken err=nil, want random source error")
	}
	if token != "" {
		t.Fatalf("token=%q, want empty token on random source error", token)
	}
}
