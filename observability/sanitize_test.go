package observability

import (
	"strings"
	"testing"
)

func TestSanitizeTextRedactsQuotedJSONCredentials(t *testing.T) {
	secret := "super-secret-value"
	got := SanitizeText(`{"access_token":"` + secret + `","authorization":"Bearer ` + secret + `"}`)

	if strings.Contains(got, secret) {
		t.Fatalf("SanitizeText leaked quoted JSON credential: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("SanitizeText result=%q, want redaction marker", got)
	}
}
