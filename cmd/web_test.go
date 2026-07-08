package cmd

import (
	"errors"
	"testing"
)

type failingWebTokenReader struct{}

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
