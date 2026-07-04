package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestDownloadFileRejectsOversizedContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "134217729")
		_, _ = w.Write([]byte("too large"))
	}))
	defer server.Close()

	_, err := downloadFile(server.URL)
	if err == nil {
		t.Fatal("downloadFile error = nil, want oversized download error")
	}
}

func TestUpdateRestartFlagDefaultsFalse(t *testing.T) {
	if updateRestartFlag {
		t.Fatal("update should not restart service unless --restart is set")
	}
}

func TestValidateUpdateTargetRejectsDifferentRunningExecutable(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	runningPath := filepath.Join(t.TempDir(), "running-weclaw")
	updatePath := filepath.Join(t.TempDir(), "update-weclaw")
	if err := os.WriteFile(runningPath, []byte("running"), 0o755); err != nil {
		t.Fatalf("write running path: %v", err)
	}
	if err := os.WriteFile(updatePath, []byte("update"), 0o755); err != nil {
		t.Fatalf("write update path: %v", err)
	}
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: runningPath}); err != nil {
		t.Fatalf("writeRuntimeState error: %v", err)
	}

	err := validateUpdateTargetMatchesRuntime(updatePath)

	if err == nil {
		t.Fatal("validateUpdateTargetMatchesRuntime error = nil, want path mismatch")
	}
	if !strings.Contains(err.Error(), runningPath) || !strings.Contains(err.Error(), updatePath) {
		t.Fatalf("error=%v, want both paths", err)
	}
}
