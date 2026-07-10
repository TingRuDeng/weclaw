package ilink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientMethodsRejectBusinessErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":17,"errmsg":"denied"}`))
	}))
	defer server.Close()
	client := NewClient(&Credentials{BaseURL: server.URL})

	tests := []struct {
		name string
		call func() error
	}{
		{name: "send message", call: func() error {
			_, err := client.SendMessage(context.Background(), &SendMessageRequest{})
			return err
		}},
		{name: "get config", call: func() error {
			_, err := client.GetConfig(context.Background(), "user", "token")
			return err
		}},
		{name: "get upload url", call: func() error {
			_, err := client.GetUploadURL(context.Background(), &GetUploadURLRequest{})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil {
				t.Fatal("business error should be returned")
			}
		})
	}
}
