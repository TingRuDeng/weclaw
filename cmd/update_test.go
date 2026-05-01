package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubRepoUsesProjectFork(t *testing.T) {
	if githubRepo != "TingRuDeng/weclaw" {
		t.Fatalf("githubRepo = %q, want TingRuDeng/weclaw", githubRepo)
	}
}

func TestNewGitHubRequestUsesGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "token-1")
	t.Setenv("GH_TOKEN", "")

	req, err := newGitHubRequest("GET", "https://api.github.com/repos/TingRuDeng/weclaw/releases/latest")
	if err != nil {
		t.Fatalf("newGitHubRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer token-1" {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
	if got := req.Header.Get("User-Agent"); got != githubUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, githubUserAgent)
	}
}

func TestGitHubAuthTokenFallsBackToGHToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "token-2")

	if got := githubAuthToken(); got != "token-2" {
		t.Fatalf("githubAuthToken = %q, want token-2", got)
	}
}

func TestReleaseTagFromLatestRedirect(t *testing.T) {
	location := "https://github.com/TingRuDeng/weclaw/releases/tag/v0.1.3"

	got, err := releaseTagFromLatestRedirect(location)
	if err != nil {
		t.Fatalf("releaseTagFromLatestRedirect error: %v", err)
	}
	if got != "v0.1.3" {
		t.Fatalf("tag = %q, want v0.1.3", got)
	}
}

func TestReleaseTagFromLatestRedirectRejectsInvalidLocation(t *testing.T) {
	if _, err := releaseTagFromLatestRedirect("https://github.com/TingRuDeng/weclaw/releases"); err == nil {
		t.Fatal("expected invalid redirect error")
	}
}

func TestParseReleaseChecksumsFindsAsset(t *testing.T) {
	checksums := "abc123  weclaw_darwin_arm64\nzzz  weclaw_linux_amd64\n"

	got, err := parseReleaseChecksums(checksums, "weclaw_darwin_arm64")
	if err != nil {
		t.Fatalf("parseReleaseChecksums error: %v", err)
	}
	if got != "abc123" {
		t.Fatalf("checksum = %q, want abc123", got)
	}
}

func TestVerifyDownloadedAssetChecksumRejectsMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weclaw_darwin_arm64")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write temp asset: %v", err)
	}

	err := verifyDownloadedAssetChecksum(path, "0000")
	if err == nil {
		t.Fatal("verifyDownloadedAssetChecksum error = nil, want mismatch")
	}
}

func TestUpdateRestartFlagDefaultsFalse(t *testing.T) {
	if updateRestartFlag {
		t.Fatal("update should not restart service unless --restart is set")
	}
}
