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

func TestSanitizeTextRedactsCommonCredentialShapes(t *testing.T) {
	tests := []string{
		`app_secret=synthetic-app-secret`,
		`tenant_access_token: synthetic-tenant-token`,
		`OPENAI_API_KEY=sk-synthetic-token-123456`,
		`client_secret：synthetic-client-secret`,
		`authorization＝synthetic-authorization`,
		`https://user:synthetic-password@example.com/path`,
		`-----BEGIN PRIVATE KEY----- synthetic-key -----END PRIVATE KEY-----`,
	}
	for _, input := range tests {
		got := SanitizeText(input)
		if strings.Contains(got, "synthetic") {
			t.Errorf("SanitizeText leaked credential shape %q as %q", input, got)
		}
	}
}
