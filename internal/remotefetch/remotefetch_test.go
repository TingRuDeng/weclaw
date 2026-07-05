package remotefetch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateURLRejectsUnsafeHosts(t *testing.T) {
	for _, rawURL := range []string{
		"http://localhost/a.png",
		"http://127.0.0.1/a.png",
		"http://10.0.0.1/a.png",
		"file:///tmp/a.png",
	} {
		if err := ValidateURL(rawURL); err == nil {
			t.Fatalf("ValidateURL(%q) error = nil, want rejection", rawURL)
		}
	}
}

func TestNewHTTPClientRedirectRejectsUnsafeTarget(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/private.png", nil)
	client := NewHTTPClient(DefaultOptions())

	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("CheckRedirect() error = nil, want loopback rejection")
	}
}

func TestReadBodyRejectsBodyAboveLimit(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("123456789")),
		ContentLength: -1,
	}

	_, err := ReadBody(resp, 8)
	if err == nil {
		t.Fatal("ReadBody() error = nil, want body size rejection")
	}
}
