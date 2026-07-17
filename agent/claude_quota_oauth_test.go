package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseClaudeOAuthCredentialSupportsKnownKeys(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "current", json: `{"claudeAiOauth":{"accessToken":"token-current","expiresAt":123}}`},
		{name: "legacy", json: `{"claude.ai_oauth":{"accessToken":"token-legacy"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := parseClaudeOAuthCredential([]byte(tt.json))
			if err != nil {
				t.Fatalf("parseClaudeOAuthCredential error: %v", err)
			}
			if token != "token-"+tt.name {
				t.Fatalf("token=%q", token)
			}
		})
	}
}

func TestParseClaudeOAuthCredentialRejectsMissingToken(t *testing.T) {
	for _, input := range []string{`{}`, `{"claudeAiOauth":{}}`, `{"claudeAiOauth":{"accessToken":"  "}}`} {
		if _, err := parseClaudeOAuthCredential([]byte(input)); err == nil {
			t.Fatalf("input=%s: expected error", input)
		}
	}
}

func TestClaudeLegacyCredentialFileUsesConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"file-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &ACPAgent{env: map[string]string{"CLAUDE_CONFIG_DIR": dir}}

	gotPath, err := a.claudeLegacyCredentialsPath()
	if err != nil {
		t.Fatalf("claudeLegacyCredentialsPath error: %v", err)
	}
	if gotPath != path {
		t.Fatalf("path=%q, want %q", gotPath, path)
	}
	data, found, err := a.readClaudeLegacyCredentialFile(context.Background(), gotPath)
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	token, err := parseClaudeOAuthCredential(data)
	if err != nil || token != "file-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}

func TestClaudeQuotaEnvironmentHonorsRunAsPreserveList(t *testing.T) {
	a := &ACPAgent{
		env:   map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "isolated-token", "CLAUDE_CONFIG_DIR": "/parent/config"},
		runAs: runAsUserSpec{User: "weclaw-quota-other-user", PreserveEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
	}
	if !a.runAs.shouldIsolate() {
		t.Skip("test target unexpectedly matches the current user")
	}
	if token, ok := a.claudeQuotaEnvValue("CLAUDE_CODE_OAUTH_TOKEN"); !ok || token != "isolated-token" {
		t.Fatalf("preserved token=%q ok=%t", token, ok)
	}
	if configDir, ok := a.claudeQuotaEnvValue("CLAUDE_CONFIG_DIR"); ok || configDir != "" {
		t.Fatalf("unpreserved config dir=%q ok=%t", configDir, ok)
	}
}

func TestQueryClaudeOAuthQuotaUsesExpectedRequestAndParsesWindows(t *testing.T) {
	const token = "test-oauth-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/usage" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != claudeOAuthUsageBeta {
			t.Errorf("anthropic-beta=%q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"seven_day":{"utilization":40,"resets_at":"2026-07-20T08:00:00Z"},
			"five_hour":{"utilization":12.5,"resets_at":"2026-07-17T08:00:00Z"},
			"z_new":{"utilization":80,"resets_at":"2026-07-22T08:00:00Z"},
			"a_new":{"utilization":20},
			"metadata":{"message":"not a quota window"},
			"limits":[],
			"extra_usage":{"is_enabled":true,"monthly_limit":50,"used_credits":12.5,"utilization":25,"currency":"USD"}
		}`))
	}))
	defer server.Close()

	quota, err := queryClaudeOAuthQuota(context.Background(), server.Client(), server.URL+"/api/oauth/usage", token)
	if err != nil {
		t.Fatalf("queryClaudeOAuthQuota error: %v", err)
	}
	if !quota.RateLimitsAvailable {
		t.Fatalf("quota=%+v", quota)
	}
	wantIDs := []string{"five_hour", "seven_day", "a_new", "z_new"}
	if len(quota.Limits) != len(wantIDs) {
		t.Fatalf("limits=%+v", quota.Limits)
	}
	for i, want := range wantIDs {
		if quota.Limits[i].ID != want {
			t.Fatalf("limits[%d].ID=%q, want %q", i, quota.Limits[i].ID, want)
		}
	}
	if quota.ExtraUsage == nil || !quota.ExtraUsage.Enabled || quota.ExtraUsage.MonthlyLimit == nil || *quota.ExtraUsage.MonthlyLimit != 50 || quota.ExtraUsage.UsedCredits == nil || *quota.ExtraUsage.UsedCredits != 12.5 || quota.ExtraUsage.Currency != "USD" {
		t.Fatalf("extra usage=%+v", quota.ExtraUsage)
	}
}

func TestQueryClaudeOAuthQuotaDoesNotExposeErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("sensitive-upstream-body"))
	}))
	defer server.Close()

	_, err := queryClaudeOAuthQuota(context.Background(), server.Client(), server.URL, "test-oauth-token")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error=%v", err)
	}
	for _, secret := range []string{"sensitive-upstream-body", "test-oauth-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestClaudeQuotaHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	_, err := queryClaudeOAuthQuota(context.Background(), newClaudeQuotaHTTPClient(), source.URL, "redirect-secret")
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("error=%v", err)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests=%d, want 0", got)
	}
}
