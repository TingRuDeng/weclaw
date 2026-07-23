package remotefetch

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type sequenceResolver struct {
	results [][]net.IPAddr
	calls   int
}

func (r *sequenceResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	index := r.calls
	r.calls++
	if index >= len(r.results) {
		index = len(r.results) - 1
	}
	return r.results[index], nil
}

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

func TestResolveSafeIPRejectsDNSRebindingOnLaterLookup(t *testing.T) {
	resolver := &sequenceResolver{results: [][]net.IPAddr{
		{{IP: net.ParseIP("8.8.8.8")}},
		{{IP: net.ParseIP("127.0.0.1")}},
	}}
	if ip, err := resolveSafeIPWithResolver(context.Background(), resolver, "media.example"); err != nil || ip.String() != "8.8.8.8" {
		t.Fatalf("first lookup ip=%s err=%v, want public address", ip, err)
	}
	if _, err := resolveSafeIPWithResolver(context.Background(), resolver, "media.example"); err == nil {
		t.Fatal("later lookup after DNS rebinding must reject loopback")
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls=%d, want a fresh lookup per dial", resolver.calls)
	}
}

func TestResolveSafeIPRejectsMixedPublicAndPrivateAnswers(t *testing.T) {
	resolver := &sequenceResolver{results: [][]net.IPAddr{{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("10.0.0.1")},
	}}}
	if _, err := resolveSafeIPWithResolver(context.Background(), resolver, "mixed.example"); err == nil {
		t.Fatal("mixed public and private DNS answers must fail closed")
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
