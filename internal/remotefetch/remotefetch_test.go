package remotefetch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func TestValidateIPRejectsSpecialPurposeRanges(t *testing.T) {
	blocked := []string{
		"0.1.2.3",
		"100.64.0.1",
		"100.100.100.200",
		"192.0.2.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"240.0.0.1",
		"64:ff9b::1",
		"64:ff9b:1::1",
		"100::1",
		"2001:db8::1",
		"2002:a00:1::1",
	}
	for _, rawIP := range blocked {
		t.Run(rawIP, func(t *testing.T) {
			if err := validateIP(netip.MustParseAddr(rawIP)); err == nil {
				t.Fatalf("validateIP(%s) error=nil, want special-purpose rejection", rawIP)
			}
		})
	}
}

func TestValidateIPAllowsPublicAddresses(t *testing.T) {
	for _, rawIP := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if err := validateIP(netip.MustParseAddr(rawIP)); err != nil {
			t.Fatalf("validateIP(%s) error=%v, want public address allowed", rawIP, err)
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
