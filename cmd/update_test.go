package cmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGitHubRepoUsesProjectFork(t *testing.T) {
	if githubRepo != "TingRuDeng/weclaw" {
		t.Fatalf("githubRepo = %q, want TingRuDeng/weclaw", githubRepo)
	}
}

func TestParseReleaseSource(t *testing.T) {
	for _, test := range []struct {
		input string
		want  releaseSource
	}{
		{input: "", want: releaseSourceAuto},
		{input: " auto ", want: releaseSourceAuto},
		{input: "github", want: releaseSourceGitHub},
		{input: "GITEE", want: releaseSourceGitee},
	} {
		got, err := parseReleaseSource(test.input)
		if err != nil || got != test.want {
			t.Fatalf("parseReleaseSource(%q)=(%q,%v), want %q", test.input, got, err, test.want)
		}
	}

	if _, err := parseReleaseSource("proxy"); err == nil {
		t.Fatal("parseReleaseSource(proxy) error=nil, want unsupported source")
	}
}

func TestGiteeLatestVersionFromBase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/jimdeng891/weclaw/releases/latest" {
			t.Fatalf("path=%q, want Gitee latest release endpoint", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.2.3"})
	}))
	defer server.Close()

	got, err := getGiteeLatestVersionFromBase(server.URL)
	if err != nil || got != "v1.2.3" {
		t.Fatalf("getGiteeLatestVersionFromBase=(%q,%v), want v1.2.3", got, err)
	}
}

func TestReleaseAssetURLForSource(t *testing.T) {
	for _, test := range []struct {
		source releaseSource
		name   string
		want   string
	}{
		{source: releaseSourceGitHub, name: "weclaw_linux_amd64", want: "https://github.com/TingRuDeng/weclaw/releases/download/v1.2.3/weclaw_linux_amd64"},
		{source: releaseSourceGitee, name: "weclaw_linux_amd64", want: "https://gitee.com/jimdeng891/weclaw/releases/download/v1.2.3/weclaw_linux_amd64.gz"},
		{source: releaseSourceGitee, name: "checksums.txt", want: "https://gitee.com/jimdeng891/weclaw/releases/download/v1.2.3/checksums.txt"},
	} {
		got, err := releaseAssetURLForSource(test.source, "v1.2.3", test.name)
		if err != nil || got != test.want {
			t.Fatalf("releaseAssetURLForSource(%q)=(%q,%v), want %q", test.source, got, err, test.want)
		}
	}
}

func TestDecompressGiteeReleaseAsset(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "weclaw_linux_amd64.gz")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(archive)
	if _, err := writer.Write([]byte("verified-binary")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	path, err := decompressGiteeReleaseAsset(archivePath)
	if err != nil {
		t.Fatalf("decompressGiteeReleaseAsset error: %v", err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "verified-binary" {
		t.Fatalf("decompressed=(%q,%v), want verified binary", data, err)
	}
}

func TestResolveLatestReleaseAutoFallsBackOnlyOnAvailabilityError(t *testing.T) {
	githubCalls := 0
	giteeCalls := 0
	version, sources, err := resolveLatestRelease(
		releaseSourceAuto,
		func() (string, error) {
			githubCalls++
			return "", releaseUnavailable(errors.New("TLS handshake timeout"))
		},
		func() (string, error) {
			giteeCalls++
			return "v1.2.3", nil
		},
	)
	if err != nil || version != "v1.2.3" || !reflect.DeepEqual(sources, []releaseSource{releaseSourceGitee}) {
		t.Fatalf("resolveLatestRelease=(%q,%v,%v), want Gitee v1.2.3", version, sources, err)
	}
	if githubCalls != 1 || giteeCalls != 1 {
		t.Fatalf("calls=(github:%d,gitee:%d), want one each", githubCalls, giteeCalls)
	}
}

func TestResolveLatestReleaseAutoDoesNotHideGitHubIntegrityError(t *testing.T) {
	giteeCalls := 0
	_, _, err := resolveLatestRelease(
		releaseSourceAuto,
		func() (string, error) { return "", errors.New("invalid latest tag") },
		func() (string, error) { giteeCalls++; return "v1.2.3", nil },
	)
	if err == nil || giteeCalls != 0 {
		t.Fatalf("error=%v giteeCalls=%d, want fail closed without fallback", err, giteeCalls)
	}
}

func TestResolveLatestReleaseKeepsGiteeAsAssetFallback(t *testing.T) {
	version, sources, err := resolveLatestRelease(
		releaseSourceAuto,
		func() (string, error) { return "v1.2.3", nil },
		func() (string, error) { t.Fatal("latest must not query Gitee after GitHub succeeds"); return "", nil },
	)
	if err != nil || version != "v1.2.3" || !reflect.DeepEqual(sources, []releaseSource{releaseSourceGitHub, releaseSourceGitee}) {
		t.Fatalf("resolveLatestRelease=(%q,%v,%v), want GitHub then Gitee asset candidates", version, sources, err)
	}
}

func TestTryReleaseSourcesDoesNotFallbackOnChecksumFailure(t *testing.T) {
	var calls []releaseSource
	_, err := tryReleaseSources([]releaseSource{releaseSourceGitHub, releaseSourceGitee}, func(source releaseSource) (string, error) {
		calls = append(calls, source)
		return "", errors.New("checksum mismatch")
	})
	if err == nil || !reflect.DeepEqual(calls, []releaseSource{releaseSourceGitHub}) {
		t.Fatalf("error=%v calls=%v, want integrity failure without fallback", err, calls)
	}
}

func TestTryReleaseSourcesFallsBackOnAvailabilityError(t *testing.T) {
	var calls []releaseSource
	got, err := tryReleaseSources([]releaseSource{releaseSourceGitHub, releaseSourceGitee}, func(source releaseSource) (string, error) {
		calls = append(calls, source)
		if source == releaseSourceGitHub {
			return "", releaseUnavailable(errors.New("connection reset"))
		}
		return "verified-gitee-asset", nil
	})
	if err != nil || got != "verified-gitee-asset" || !reflect.DeepEqual(calls, []releaseSource{releaseSourceGitHub, releaseSourceGitee}) {
		t.Fatalf("got=%q error=%v calls=%v, want Gitee fallback", got, err, calls)
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

func TestNewGitHubRequestDoesNotLeakTokenToGitee(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "must-not-leak")
	req, err := newGitHubRequest(http.MethodGet, "https://gitee.com/jimdeng891/weclaw/releases/latest")
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization=%q, want empty for non-GitHub host", got)
	}
}

func TestUpdateReleaseTagOverrideAcceptsStableTag(t *testing.T) {
	t.Setenv(updateReleaseTagEnv, " v9.8.7 ")

	tag, overridden, err := updateReleaseTagOverride()

	if err != nil || !overridden || tag != "v9.8.7" {
		t.Fatalf("override=(%q,%t,%v), want stable explicit tag", tag, overridden, err)
	}
}

func TestUpdateReleaseTagOverrideRejectsUnsafeTag(t *testing.T) {
	t.Setenv(updateReleaseTagEnv, `v1.2.3/../../asset`)

	if _, overridden, err := updateReleaseTagOverride(); err == nil || !overridden {
		t.Fatalf("override=(%t,%v), want validation failure", overridden, err)
	}
}

func TestUpdateReleaseTagOverrideIsOptional(t *testing.T) {
	t.Setenv(updateReleaseTagEnv, "")

	if tag, overridden, err := updateReleaseTagOverride(); err != nil || overridden || tag != "" {
		t.Fatalf("override=(%q,%t,%v), want latest-release path", tag, overridden, err)
	}
}

func TestFindGitHubReleaseAssetAPIURLRejectsUnexpectedHost(t *testing.T) {
	release := githubRelease{Assets: []githubReleaseAsset{
		{Name: "checksums.txt", URL: "https://attacker.invalid/checksums.txt"},
		{Name: "weclaw_darwin_arm64", URL: "https://api.github.com/repos/TingRuDeng/weclaw/releases/assets/42"},
	}}

	got, err := findGitHubReleaseAssetAPIURL(release, "v1.2.3", "weclaw_darwin_arm64")
	if err != nil || got != release.Assets[1].URL {
		t.Fatalf("asset URL=(%q,%v), want GitHub API asset", got, err)
	}
	if _, err := findGitHubReleaseAssetAPIURL(release, "v1.2.3", "checksums.txt"); err == nil {
		t.Fatal("unexpected asset host must be rejected")
	}
}

func TestGitHubReleaseAssetAPIURLFindsDraftFromAuthenticatedReleaseList(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "draft-token")
	t.Setenv("GH_TOKEN", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/TingRuDeng/weclaw/releases" {
			t.Fatalf("path=%q, want releases collection", r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Fatalf("per_page=%q, want 100", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer draft-token" {
			t.Fatalf("Authorization=%q, want draft token", got)
		}
		_ = json.NewEncoder(w).Encode([]githubRelease{
			{TagName: "v1.2.2"},
			{
				TagName: "v1.2.3",
				Assets: []githubReleaseAsset{{
					Name: "weclaw_darwin_arm64",
					URL:  "https://api.github.com/repos/TingRuDeng/weclaw/releases/assets/42",
				}},
			},
		})
	}))
	defer server.Close()

	got, err := githubReleaseAssetAPIURLFromBase(server.URL, "v1.2.3", "weclaw_darwin_arm64")
	if err != nil {
		t.Fatalf("githubReleaseAssetAPIURLFromBase error=%v", err)
	}
	if want := "https://api.github.com/repos/TingRuDeng/weclaw/releases/assets/42"; got != want {
		t.Fatalf("asset URL=%q, want %q", got, want)
	}
}

func TestDownloadFileWithAcceptSetsReleaseAssetHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/octet-stream" {
			t.Fatalf("Accept=%q, want release asset media type", got)
		}
		_, _ = w.Write([]byte("asset"))
	}))
	defer server.Close()

	path, err := downloadFileWithAccept(server.URL, "application/octet-stream")
	if err != nil {
		t.Fatalf("downloadFileWithAccept error=%v", err)
	}
	defer os.Remove(path)
}

func TestDownloadFileAllowsReleaseAssetTransferBeyondMetadataWindow(t *testing.T) {
	originalTransport := updateHTTPClient.Transport
	var remaining time.Duration
	updateHTTPClient.Transport = updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("release asset request has no transfer deadline")
		}
		remaining = time.Until(deadline)
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          http.NoBody,
			ContentLength: 0,
			Request:       req,
		}, nil
	})
	t.Cleanup(func() {
		updateHTTPClient.Transport = originalTransport
	})

	path, err := downloadFile("https://github.com/TingRuDeng/weclaw/releases/download/v1.2.3/weclaw_darwin_arm64")
	if err != nil {
		t.Fatalf("downloadFile error=%v", err)
	}
	defer os.Remove(path)
	if remaining < 5*time.Minute {
		t.Fatalf("asset transfer deadline=%s, want at least 5m", remaining)
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

func TestParseReleaseChecksumsRejectsDuplicateAsset(t *testing.T) {
	checksums := "abc123  weclaw_linux_amd64\ndef456  weclaw_linux_amd64\n"
	if _, err := parseReleaseChecksums(checksums, "weclaw_linux_amd64"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("parseReleaseChecksums error=%v, want duplicate rejection", err)
	}
}

func TestDownloadFileClassifiesOnlyServerFailuresAsUnavailable(t *testing.T) {
	for _, test := range []struct {
		status      int
		unavailable bool
	}{
		{status: http.StatusBadGateway, unavailable: true},
		{status: http.StatusNotFound, unavailable: false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.status)
		}))
		_, err := downloadFile(server.URL)
		server.Close()
		if err == nil || isReleaseUnavailable(err) != test.unavailable {
			t.Fatalf("HTTP %d error=%v unavailable=%t, want %t", test.status, err, isReleaseUnavailable(err), test.unavailable)
		}
	}
}

func TestEffectiveReleaseSourceFlagOverridesConfig(t *testing.T) {
	got, err := effectiveReleaseSource("github", "gitee")
	if err != nil || got != releaseSourceGitHub {
		t.Fatalf("effectiveReleaseSource=(%q,%v), want github", got, err)
	}
	got, err = effectiveReleaseSource("", "gitee")
	if err != nil || got != releaseSourceGitee {
		t.Fatalf("effectiveReleaseSource=(%q,%v), want configured gitee", got, err)
	}
}

func TestReleaseAssetNameSupportsDarwinArm64AndLinuxAMD64(t *testing.T) {
	for _, supported := range [][3]string{
		{"darwin", "arm64", "weclaw_darwin_arm64"},
		{"linux", "amd64", "weclaw_linux_amd64"},
	} {
		name, err := releaseAssetNameForRuntime(supported[0], supported[1])
		if err != nil {
			t.Fatalf("releaseAssetNameForRuntime(%q, %q) error: %v", supported[0], supported[1], err)
		}
		if name != supported[2] {
			t.Fatalf("asset name=%q, want %q", name, supported[2])
		}
	}

	for _, unsupported := range [][2]string{{"darwin", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
		if _, err := releaseAssetNameForRuntime(unsupported[0], unsupported[1]); err == nil {
			t.Fatalf("releaseAssetNameForRuntime(%q, %q) error=nil", unsupported[0], unsupported[1])
		} else if !strings.Contains(err.Error(), "darwin/arm64、linux/amd64") {
			t.Fatalf("error=%v, want published-target hint", err)
		}
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

func TestFinishUpdateSkipsApplyAndPreflightWhenAlreadyLatest(t *testing.T) {
	applied := false
	prepared := false
	var out bytes.Buffer
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			prepared = true
			return preparedStart{}, nil
		},
		out: &out,
	}

	err := finishUpdate(
		context.Background(), "v0.1.181", "v0.1.181", false, false,
		func(string) (updateTransaction, error) { applied = true; return updateTransaction{}, nil }, ops, &out,
	)

	if err != nil {
		t.Fatalf("finishUpdate error=%v", err)
	}
	if applied || prepared {
		t.Fatalf("applied=%t prepared=%t，最新版不应下载或执行启动预检", applied, prepared)
	}
	if !strings.Contains(out.String(), "已是最新版本 (v0.1.181)") {
		t.Fatalf("output=%q，want latest version", out.String())
	}
}

func TestFinishUpdateAlreadyLatestStillPreflightsExplicitRestart(t *testing.T) {
	applied := false
	prepared := false
	var out bytes.Buffer
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			prepared = true
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { return nil },
		running:    func() bool { return false },
		stop:       func() error { t.Fatal("服务未运行时不应停止"); return nil },
		out:        &out,
	}

	err := finishUpdate(
		context.Background(), "v0.1.181", "v0.1.181", true, false,
		func(string) (updateTransaction, error) { applied = true; return updateTransaction{}, nil }, ops, &out,
	)

	if err != nil {
		t.Fatalf("finishUpdate error=%v", err)
	}
	if applied || !prepared {
		t.Fatalf("applied=%t prepared=%t，显式 restart 应跳过下载但保留预检", applied, prepared)
	}
}

func TestFinishUpdateAppliesNewVersionBeforePreflight(t *testing.T) {
	var calls []string
	committed := false
	var out bytes.Buffer
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			calls = append(calls, "prepare")
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		out: &out,
	}

	err := finishUpdate(
		context.Background(), "v0.1.180", "v0.1.181", false, false,
		func(version string) (updateTransaction, error) {
			if version != "v0.1.181" {
				t.Fatalf("apply version=%q", version)
			}
			calls = append(calls, "apply")
			return updateTransaction{commit: func() {
				calls = append(calls, "commit")
				committed = true
			}}, nil
		},
		ops,
		&out,
	)

	if err != nil {
		t.Fatalf("finishUpdate error=%v", err)
	}
	if !reflect.DeepEqual(calls, []string{"apply", "prepare", "commit"}) || !committed {
		t.Fatalf("calls=%v committed=%t，want apply, prepare, then commit", calls, committed)
	}
}

func TestFinishUpdateRejectsDowngrade(t *testing.T) {
	applied := false
	err := finishUpdate(
		context.Background(), "v0.1.10", "v0.1.9", false, false,
		func(string) (updateTransaction, error) {
			applied = true
			return updateTransaction{}, nil
		},
		updateCompletionOps{},
		&bytes.Buffer{},
	)

	if err == nil || !strings.Contains(err.Error(), "降级") {
		t.Fatalf("finishUpdate error=%v, want downgrade rejection", err)
	}
	if applied {
		t.Fatal("downgrade must not replace the binary")
	}
}

// TestCompleteUpdateHandlesClaudeACPPreflight 验证普通更新警告与更新后重启阻断使用同一预检。
func TestCompleteUpdateHandlesClaudeACPPreflight(t *testing.T) {
	want := errors.New("ACP 能力缺失")
	stopped := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) { return preparedStart{}, want },
		ensureSafe: func(context.Context, bool, *config.Config) error {
			t.Fatal("预检失败后不应检查任务")
			return nil
		},
		running: func() bool { t.Fatal("预检失败后不应检查进程"); return false },
		stop:    func() error { stopped = true; return nil }, out: &bytes.Buffer{},
	}
	if err := completeUpdate(context.Background(), true, false, ops); !errors.Is(err, want) || stopped {
		t.Fatalf("restart error=%v stopped=%t, want preflight failure without stop", err, stopped)
	}
	var out bytes.Buffer
	ops.out = &out
	if err := completeUpdate(context.Background(), false, false, ops); err != nil {
		t.Fatalf("ordinary update error=%v, want warning only", err)
	}
	if !strings.Contains(out.String(), "警告") || !strings.Contains(out.String(), want.Error()) {
		t.Fatalf("ordinary update output=%q, want dependency warning", out.String())
	}
}

func TestCompleteUpdateRollsBackWhenCodexCLILeaseBlocksRestart(t *testing.T) {
	rolledBack := false
	cancelled := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return agent.ErrCodexCLIFrontendActive },
		cancelDrain: func(context.Context, *config.Config) error { cancelled = true; return nil },
		out:         &bytes.Buffer{},
	}
	err := completeUpdateWithRollback(context.Background(), true, false, ops, func() error {
		rolledBack = true
		return nil
	})
	if !errors.Is(err, agent.ErrCodexCLIFrontendActive) || !rolledBack || !cancelled {
		t.Fatalf("error=%v rolledBack=%v cancelled=%v", err, rolledBack, cancelled)
	}
}

func TestCompleteUpdateRestartDelegatesSystemd(t *testing.T) {
	var calls []string
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			calls = append(calls, "prepare")
			return preparedStart{cfg: config.DefaultConfig(), run: func() error {
				t.Fatal("systemd-managed update must not spawn private daemon")
				return nil
			}}, nil
		},
		ensureSafe: func(context.Context, bool, *config.Config) error { calls = append(calls, "drain"); return nil },
		running:    func() bool { calls = append(calls, "running"); return true },
		isSystemd:  func() bool { calls = append(calls, "systemd"); return true },
		restartSystemd: func() error {
			calls = append(calls, "restart-systemd")
			return nil
		},
		stop: func() error {
			t.Fatal("systemd-managed update must not stop by pid and spawn a daemon")
			return nil
		},
		out: &bytes.Buffer{},
	}
	if err := completeUpdate(context.Background(), true, false, ops); err != nil {
		t.Fatalf("completeUpdate: %v", err)
	}
	want := []string{"prepare", "drain", "running", "systemd", "restart-systemd"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
}

func TestCompleteUpdateRestartCancelsDrainWhenStopFails(t *testing.T) {
	wantErr := errors.New("stop failed")
	cancelled := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return nil },
		running:     func() bool { return true },
		isSystemd:   func() bool { return false },
		stop:        func() error { return wantErr },
		cancelDrain: func(context.Context, *config.Config) error { cancelled = true; return nil },
		out:         &bytes.Buffer{},
	}
	if err := completeUpdate(context.Background(), true, false, ops); !errors.Is(err, wantErr) || !cancelled {
		t.Fatalf("error=%v cancelled=%v, want failed stop to restore admission", err, cancelled)
	}
}

func TestCompleteUpdateRestartCancelsDrainWhenSystemdRestartIsUnavailable(t *testing.T) {
	cancelled := false
	ops := updateCompletionOps{
		prepare: func(context.Context) (preparedStart, error) {
			return preparedStart{cfg: config.DefaultConfig()}, nil
		},
		ensureSafe:  func(context.Context, bool, *config.Config) error { return nil },
		running:     func() bool { return true },
		isSystemd:   func() bool { return true },
		cancelDrain: func(context.Context, *config.Config) error { cancelled = true; return nil },
		out:         &bytes.Buffer{},
	}
	if err := completeUpdate(context.Background(), true, false, ops); err == nil || !cancelled {
		t.Fatalf("error=%v cancelled=%v, want unavailable supervisor to restore admission", err, cancelled)
	}
}

func TestReplaceBinaryUsesAtomicTargetDirectoryStage(t *testing.T) {
	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	src := filepath.Join(sourceDir, "downloaded")
	dst := filepath.Join(targetDir, "weclaw")
	if err := os.WriteFile(src, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(src, dst); err != nil {
		t.Fatalf("replaceBinary error: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "new-binary" {
		t.Fatalf("target=%q err=%v", data, err)
	}
	matches, err := filepath.Glob(filepath.Join(targetDir, ".weclaw-update-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staged files=%#v err=%v", matches, err)
	}
}

func TestInstallBinaryWithRollbackRestoresPreviousBinary(t *testing.T) {
	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	src := filepath.Join(sourceDir, "downloaded")
	dst := filepath.Join(targetDir, "weclaw")
	if err := os.WriteFile(src, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	transaction, err := installBinaryWithRollback(src, dst)
	if err != nil {
		t.Fatalf("installBinaryWithRollback error: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "new-binary" {
		t.Fatalf("installed target=%q err=%v", data, err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("Rollback error: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "old-binary" {
		t.Fatalf("restored target=%q err=%v", data, err)
	}
}

func TestInstallBinaryCommitKeepsNewBinary(t *testing.T) {
	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	src := filepath.Join(sourceDir, "downloaded")
	dst := filepath.Join(targetDir, "weclaw")
	if err := os.WriteFile(src, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	transaction, err := installBinaryWithRollback(src, dst)
	if err != nil {
		t.Fatalf("installBinaryWithRollback error: %v", err)
	}
	transaction.Commit()
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("Rollback after commit error: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "new-binary" {
		t.Fatalf("committed target=%q err=%v", data, err)
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

func TestResolveSymlinkDetectsCycle(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.Symlink(second, first); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Fatalf("create symlink cycle: %v", err)
	}

	if _, err := resolveSymlink(first); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("resolveSymlink error=%v, want cycle error", err)
	}
}

func TestResolveSymlinkResolvesRelativeTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, err := resolveSymlink(link)
	if err != nil {
		t.Fatalf("resolveSymlink error: %v", err)
	}
	if got != target {
		t.Fatalf("resolveSymlink=%q, want %q", got, target)
	}
}

func TestRestartGuardBlocksWhenRuntimeHasActiveTasks(t *testing.T) {
	activeTasks := 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime" {
			t.Fatalf("path=%q, want /api/runtime", r.URL.Path)
		}
		json.NewEncoder(w).Encode(runtimeStatusResponse{Status: "ok", ActiveTasks: &activeTasks})
	}))
	defer server.Close()

	err := ensureRestartSafe(context.Background(), restartSafetyOptions{
		apiAddr:       strings.TrimPrefix(server.URL, "http://"),
		processExists: true,
	})

	if err == nil {
		t.Fatal("ensureRestartSafe error = nil, want active task rejection")
	}
	if !strings.Contains(err.Error(), "1 个运行中的任务") {
		t.Fatalf("error=%v, want active task count", err)
	}
}

func TestRestartGuardBlocksWhenRuntimeStatusUnavailable(t *testing.T) {
	err := ensureRestartSafe(context.Background(), restartSafetyOptions{
		apiAddr:       "127.0.0.1:1",
		processExists: true,
	})

	if err == nil {
		t.Fatal("ensureRestartSafe error = nil, want unavailable runtime rejection")
	}
	if !strings.Contains(err.Error(), "无法确认运行中任务状态") {
		t.Fatalf("error=%v, want unavailable runtime detail", err)
	}
}

func TestRestartGuardBlocksInvalidRuntimeResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "invalid json", status: http.StatusOK, body: `{`},
		{name: "empty object", status: http.StatusOK, body: `{}`},
		{name: "missing active tasks", status: http.StatusOK, body: `{"status":"ok"}`},
		{name: "missing status", status: http.StatusOK, body: `{"active_tasks":0}`},
		{name: "null active tasks", status: http.StatusOK, body: `{"status":"ok","active_tasks":null}`},
		{name: "negative active tasks", status: http.StatusOK, body: `{"status":"ok","active_tasks":-1}`},
		{name: "wrong status", status: http.StatusOK, body: `{"status":"degraded","active_tasks":0}`},
		{name: "multiple objects", status: http.StatusOK, body: `{"status":"ok","active_tasks":0}{}`},
		{name: "trailing garbage", status: http.StatusOK, body: `{"status":"ok","active_tasks":0}x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			err := ensureRestartSafe(context.Background(), restartSafetyOptions{
				apiAddr:       strings.TrimPrefix(server.URL, "http://"),
				processExists: true,
			})
			if err == nil || !strings.Contains(err.Error(), "无法确认运行中任务状态") {
				t.Fatalf("ensureRestartSafe error=%v, want runtime rejection", err)
			}
		})
	}
}

func TestRestartGuardAllowsValidatedIdleRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","active_tasks":0,"trace":{"enabled":false}}`))
	}))
	defer server.Close()

	err := ensureRestartSafe(context.Background(), restartSafetyOptions{
		apiAddr:       strings.TrimPrefix(server.URL, "http://"),
		processExists: true,
	})
	if err != nil {
		t.Fatalf("ensureRestartSafe error=%v, want validated idle runtime", err)
	}
}

func TestConfiguredRestartGuardBlocksInvalidConfigForRunningProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WECLAW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{`), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	if err := writeRuntimeState(runtimeState{PID: os.Getpid(), Exe: "weclaw"}); err != nil {
		t.Fatalf("writeRuntimeState error: %v", err)
	}

	err := ensureConfiguredRestartSafe(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "无法读取当前配置") {
		t.Fatalf("ensureConfiguredRestartSafe error=%v, want config rejection", err)
	}
	if err := ensureConfiguredRestartSafe(context.Background(), true); err != nil {
		t.Fatalf("ensureConfiguredRestartSafe force error=%v, want nil", err)
	}
}

func TestRestartGuardAllowsForceWithActiveTasks(t *testing.T) {
	err := ensureRestartSafe(context.Background(), restartSafetyOptions{
		processExists: true,
		force:         true,
	})

	if err != nil {
		t.Fatalf("ensureRestartSafe error=%v, want nil with force", err)
	}
}

func TestRuntimeStatusURLDialLoopbackForWildcardListen(t *testing.T) {
	got, err := runtimeStatusURL("http://0.0.0.0:18011")
	if err != nil {
		t.Fatalf("runtimeStatusURL error: %v", err)
	}
	if got != "http://127.0.0.1:18011/api/runtime" {
		t.Fatalf("runtime status URL=%q, want loopback URL", got)
	}
}

func TestRuntimeStatusURLProjectsLocalhostToNumericLoopback(t *testing.T) {
	for _, addr := range []string{"localhost:18011", "http://localhost:18011"} {
		got, err := runtimeStatusURL(addr)
		if err != nil {
			t.Fatalf("runtimeStatusURL(%q) error: %v", addr, err)
		}
		if got != "http://127.0.0.1:18011/api/runtime" {
			t.Fatalf("runtimeStatusURL(%q)=%q, want numeric loopback URL", addr, got)
		}
	}
}

func TestRuntimeStatusURLRejectsNonLoopbackListenAddress(t *testing.T) {
	if got, err := runtimeStatusURL("http://192.168.1.5:18011"); err == nil {
		t.Fatalf("runtimeStatusURL=%q error=nil, want non-loopback rejection", got)
	}
}
