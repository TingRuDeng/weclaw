package cmd

import (
	"bytes"
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

	"github.com/fastclaw-ai/weclaw/config"
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

func TestReleaseAssetNameOnlySupportsDarwinArm64(t *testing.T) {
	name, err := releaseAssetNameForRuntime("darwin", "arm64")
	if err != nil {
		t.Fatalf("releaseAssetNameForRuntime supported target error: %v", err)
	}
	if name != "weclaw_darwin_arm64" {
		t.Fatalf("asset name=%q, want weclaw_darwin_arm64", name)
	}

	_, err = releaseAssetNameForRuntime("linux", "amd64")
	if err == nil {
		t.Fatal("releaseAssetNameForRuntime unsupported target error = nil")
	}
	if !strings.Contains(err.Error(), "darwin/arm64") {
		t.Fatalf("error=%v, want supported target hint", err)
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
		func(string) error { applied = true; return nil }, ops, &out,
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
		func(string) error { applied = true; return nil }, ops, &out,
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
		func(version string) error {
			if version != "v0.1.181" {
				t.Fatalf("apply version=%q", version)
			}
			calls = append(calls, "apply")
			return nil
		},
		ops,
		&out,
	)

	if err != nil {
		t.Fatalf("finishUpdate error=%v", err)
	}
	if !reflect.DeepEqual(calls, []string{"apply", "prepare"}) {
		t.Fatalf("calls=%v，want apply then prepare", calls)
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

func TestRestartGuardBlocksWhenRuntimeHasActiveTasks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime" {
			t.Fatalf("path=%q, want /api/runtime", r.URL.Path)
		}
		json.NewEncoder(w).Encode(runtimeStatusResponse{ActiveTasks: 1})
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
