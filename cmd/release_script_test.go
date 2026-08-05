package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseScriptSyntaxAndHelp(t *testing.T) {
	script := releaseScriptPath(t)

	runReleaseScriptTestCommand(t, "", "bash", "-n", script)
	output := runReleaseScriptTestCommand(t, "", script, "--help")
	if !strings.Contains(output, "--next-patch") || !strings.Contains(output, "--dry-run") {
		t.Fatalf("help output missing expected options: %s", output)
	}
}

func TestGiteeMirrorScriptSyntaxAndHelp(t *testing.T) {
	script := filepath.Join("..", "scripts", "mirror_gitee_release.sh")
	runReleaseScriptTestCommand(t, "", "bash", "-n", script)
	output := runReleaseScriptTestCommand(t, "", script, "--help")
	if !strings.Contains(output, "GITEE_TOKEN") || !strings.Contains(output, "asset-dir") {
		t.Fatalf("mirror help missing token or asset-dir contract: %s", output)
	}
}

func TestGiteeMirrorUsesAttachmentEndpointAndBoundedTransfers(t *testing.T) {
	script := filepath.Join("..", "scripts", "mirror_gitee_release.sh")
	content, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{
		`attachment_json=`,
		`releases/${release_id}/attach_files`,
		`gzip -n -9`,
		`--connect-timeout`,
		`--max-time`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("mirror script missing resumable attachment contract %q", required)
		}
	}
	if strings.Contains(text, `assets = release.get("assets")`) {
		t.Fatal("Gitee source archives must not be treated as uploaded release attachments")
	}
}

func TestGiteeRepairWorkflowDownloadsAuthoritativeRelease(t *testing.T) {
	workflow := filepath.Join("..", ".github", "workflows", "mirror-gitee.yml")
	content, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{
		"workflow_dispatch:",
		`gh release download "$RELEASE_TAG"`,
		`GITEE_TOKEN: ${{ secrets.GITEE_TOKEN }}`,
		`scripts/mirror_gitee_release.sh "$RELEASE_TAG"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Gitee repair workflow missing %q", required)
		}
	}
}

func TestReleasePipelineMirrorsOnlyAfterGitHubCommit(t *testing.T) {
	releaseContent, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(releaseContent)
	committed := strings.LastIndex(text, "RELEASE_COMMITTED=1")
	mirror := strings.LastIndex(text, "mirror_gitee_release")
	if committed < 0 || mirror < 0 || mirror < committed {
		t.Fatalf("Gitee mirror must run after GitHub release is committed: committed=%d mirror=%d", committed, mirror)
	}

	workflow, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "GITEE_TOKEN: ${{ secrets.GITEE_TOKEN }}") {
		t.Fatal("release workflow must inject GITEE_TOKEN from Actions Secrets")
	}
}

func TestGiteeMirrorUsesVerifiedAssetsWithoutLeakingToken(t *testing.T) {
	root := t.TempDir()
	assetsDir := filepath.Join(root, "assets")
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	assetNames := []string{"weclaw_darwin_arm64", "weclaw_darwin_amd64", "weclaw_linux_arm64", "weclaw_linux_amd64"}
	var checksums strings.Builder
	for _, name := range assetNames {
		content := []byte("verified-" + name + "\n")
		if err := os.WriteFile(filepath.Join(assetsDir, name), content, 0o755); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		fmt.Fprintf(&checksums, "%x  %s\n", sum, name)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "checksums.txt"), []byte(checksums.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	checkJSON := filepath.Join(root, "release-check.json")
	var assetsJSON strings.Builder
	assetsJSON.WriteString(`[`)
	compressedAssetNames := make([]string, 0, len(assetNames))
	for _, name := range assetNames {
		compressedAssetNames = append(compressedAssetNames, name+".gz")
	}
	allAssets := append(compressedAssetNames, "checksums.txt")
	for index, name := range allAssets {
		if index > 0 {
			assetsJSON.WriteByte(',')
		}
		fmt.Fprintf(&assetsJSON, `{"name":%q,"browser_download_url":%q}`, name, "https://gitee.com/download/"+name)
	}
	assetsJSON.WriteString(`]`)
	if err := os.WriteFile(checkJSON, []byte(assetsJSON.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	gitCalls := filepath.Join(root, "git-calls")
	fakeGit := `#!/bin/sh
if [ "$1" = "rev-parse" ]; then printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'; exit 0; fi
if [ "$1" = "ls-remote" ]; then
  printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/heads/main\n'
  printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/tags/v9.9.9\n'
  exit 0
fi
printf '%s\n' "$*" >>"$TEST_GIT_CALLS"
`
	if err := os.WriteFile(checkJSON+".release", []byte(`{"id":42,"tag_name":"v9.9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCurl := `#!/bin/sh
output=''
previous=''
url=''
for argument do
  [ "$previous" = "-o" ] && output="$argument"
  previous="$argument"
  url="$argument"
done
printf '%s\n' "$*" >>"$TEST_CURL_CALLS"
case "$url" in
  */releases) printf '{"id":42,"tag_name":"v9.9.9"}' >"$output" ;;
  */releases/tags/*)
    if [ "${TEST_RELEASE_PROBE_NULL:-}" = "1" ]; then
      printf 'null' >"$output"
    else
      cp "$TEST_RELEASE_JSON.release" "$output"
    fi
    printf '200'
    ;;
  */attach_files)
    case "${output##*/}" in
      upload-*) : >"$TEST_ATTACH_READY"; printf '{}' >"$output" ;;
      *) if [ -f "$TEST_ATTACH_READY" ]; then cp "$TEST_RELEASE_JSON" "$output"; else printf '[]' >"$output"; fi ;;
    esac
    ;;
  https://gitee.com/download/*.gz)
    download_name=${url##*/}
    /usr/bin/gzip -n -9 -c "$TEST_ASSET_DIR/${download_name%.gz}" >"$output"
    ;;
  https://gitee.com/download/*) cp "$TEST_ASSET_DIR/${url##*/}" "$output" ;;
  *) exit 91 ;;
esac
`
	for name, content := range map[string]string{"git": fakeGit, "curl": fakeCurl} {
		path := filepath.Join(fakeBin, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	script, err := filepath.Abs(filepath.Join("..", "scripts", "mirror_gitee_release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "gitee-secret-must-not-leak"
	runMirror := func(extraEnv ...string) string {
		t.Helper()
		cmd := exec.Command(script, "v9.9.9", assetsDir)
		cmd.Env = append(os.Environ(),
			"PATH="+fakeBin+":"+os.Getenv("PATH"),
			"GITEE_TOKEN="+secret,
			"TEST_ASSET_DIR="+assetsDir,
			"TEST_RELEASE_JSON="+checkJSON,
			"TEST_ATTACH_READY="+filepath.Join(root, "attachments-ready"),
			"TEST_GIT_CALLS="+gitCalls,
			"TEST_CURL_CALLS="+filepath.Join(root, "curl-calls"),
		)
		cmd.Env = append(cmd.Env, extraEnv...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mirror script failed: %v\n%s", err, output)
		}
		return string(output)
	}
	createdOutput := runMirror("TEST_RELEASE_PROBE_NULL=1")
	if !strings.Contains(createdOutput, "创建 Gitee Release") {
		t.Fatalf("HTTP 200 + null must create the missing Gitee release: %s", createdOutput)
	}
	reusedOutput := runMirror()
	if !strings.Contains(reusedOutput, "复用已有 Gitee Release") {
		t.Fatalf("valid release response must reuse the Gitee release: %s", reusedOutput)
	}
	gitOutput, err := os.ReadFile(gitCalls)
	if err != nil {
		t.Fatal(err)
	}
	combined := createdOutput + reusedOutput + string(gitOutput)
	if strings.Contains(combined, secret) {
		t.Fatal("Gitee token leaked to output or git arguments")
	}
	if !strings.Contains(string(gitOutput), "HEAD:refs/heads/main") || !strings.Contains(createdOutput, "Gitee 镜像完成") {
		t.Fatalf("mirror evidence missing: output=%s git=%s", createdOutput, gitOutput)
	}
}

func TestReleaseScriptRejectsSkipTests(t *testing.T) {
	script := releaseScriptPath(t)
	content, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "--skip-tests") || strings.Contains(string(content), "RUN_TESTS") {
		t.Fatal("正式发布脚本不得保留跳过验证的入口")
	}
	command := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + " && parse_args v9.9.9 --skip-tests"
	output := runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", command)
	if !strings.Contains(output, "未知参数") {
		t.Fatalf("skip-tests rejection=%q", output)
	}
}

func TestReleaseScriptConfiguresProjectGoCache(t *testing.T) {
	script := releaseScriptPath(t)
	sharedRoot := t.TempDir()
	want := filepath.Join(sharedRoot, "weclaw")
	command := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + ` && ` +
		`unset GOCACHE WECLAW_GOCACHE && ` +
		`go() { echo unexpected-go-call >&2; return 9; } && ` +
		`configure_go_cache darwin ` + shellQuote(sharedRoot) + ` && printf 'CACHE=%s\n' "$GOCACHE"`

	output := runReleaseScriptTestCommand(t, "", "bash", "-c", command)
	if !strings.Contains(output, "CACHE="+want+"\n") {
		t.Fatalf("configured cache output=%q, want %q", output, want)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("configured cache directory=%q info=%v err=%v", want, info, err)
	}
}

func TestReleaseScriptGoCachePrecedenceAndPortableFallback(t *testing.T) {
	script := releaseScriptPath(t)
	override := filepath.Join(t.TempDir(), "override")
	existing := filepath.Join(t.TempDir(), "existing")
	fallback := filepath.Join(t.TempDir(), "fallback")
	sharedRoot := t.TempDir()

	tests := []struct {
		name   string
		setup  string
		goFunc string
		expect string
	}{
		{
			name:   "weclaw override wins",
			setup:  `export WECLAW_GOCACHE=` + shellQuote(override) + ` GOCACHE=` + shellQuote(existing),
			goFunc: `go() { echo unexpected-go-call >&2; return 9; }`,
			expect: override,
		},
		{
			name:   "existing exported cache wins",
			setup:  `unset WECLAW_GOCACHE && export GOCACHE=` + shellQuote(existing),
			goFunc: `go() { echo unexpected-go-call >&2; return 9; }`,
			expect: existing,
		},
		{
			name:   "linux uses go default",
			setup:  `unset WECLAW_GOCACHE GOCACHE`,
			goFunc: `go() { case "$*" in "env GOHOSTOS") echo linux ;; "env GOCACHE") echo ` + shellQuote(fallback) + ` ;; *) return 9 ;; esac; }`,
			expect: fallback,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + ` && ` +
				test.setup + ` && ` + test.goFunc + ` && ` +
				`configure_go_cache linux ` + shellQuote(sharedRoot) + ` && printf 'CACHE=%s\n' "$GOCACHE"`
			output := runReleaseScriptTestCommand(t, "", "bash", "-c", command)
			if !strings.Contains(output, "CACHE="+test.expect+"\n") {
				t.Fatalf("configured cache output=%q, want %q", output, test.expect)
			}
		})
	}
}

func TestReleaseScriptNextPatchTag(t *testing.T) {
	script := releaseScriptPath(t)
	repo := t.TempDir()
	runReleaseScriptTestCommand(t, repo, "git", "init")
	runReleaseScriptTestCommand(t, repo, "git", "config", "user.email", "test@example.com")
	runReleaseScriptTestCommand(t, repo, "git", "config", "user.name", "测试用户")

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runReleaseScriptTestCommand(t, repo, "git", "add", "README.md")
	runReleaseScriptTestCommand(t, repo, "git", "commit", "-m", "初始化")
	runReleaseScriptTestCommand(t, repo, "git", "tag", "v0.1.9")
	runReleaseScriptTestCommand(t, repo, "git", "tag", "v0.1.10")

	output := runReleaseScriptTestCommand(t, repo, "bash", "-c", "WECLAW_RELEASE_SOURCE_ONLY=1 source "+shellQuote(script)+" && next_patch_tag")
	if strings.TrimSpace(output) != "v0.1.11" {
		t.Fatalf("next patch tag=%q, want v0.1.11", strings.TrimSpace(output))
	}
}

func TestReleaseScriptRequiresCurrentOriginMain(t *testing.T) {
	script := releaseScriptPath(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	repo := t.TempDir()
	runReleaseScriptTestCommand(t, "", "git", "init", "--bare", origin)
	runReleaseScriptTestCommand(t, repo, "git", "init")
	runReleaseScriptTestCommand(t, repo, "git", "config", "user.email", "test@example.com")
	runReleaseScriptTestCommand(t, repo, "git", "config", "user.name", "测试用户")

	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runReleaseScriptTestCommand(t, repo, "git", "add", "README.md")
	runReleaseScriptTestCommand(t, repo, "git", "commit", "-m", "初始化")
	runReleaseScriptTestCommand(t, repo, "git", "branch", "-M", "main")
	runReleaseScriptTestCommand(t, repo, "git", "remote", "add", "origin", origin)
	runReleaseScriptTestCommand(t, repo, "git", "push", "-u", "origin", "main")

	checkCommand := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + " && check_release_source"
	runReleaseScriptTestCommand(t, repo, "bash", "-c", checkCommand)

	runReleaseScriptTestCommand(t, repo, "git", "switch", "-c", "feature")
	output := runReleaseScriptTestCommandExpectFailure(t, repo, "bash", "-c", checkCommand)
	if !strings.Contains(output, "main 分支") {
		t.Fatalf("non-main rejection=%q, want branch hint", output)
	}

	runReleaseScriptTestCommand(t, repo, "git", "switch", "main")
	if err := os.WriteFile(readme, []byte("ahead\n"), 0o644); err != nil {
		t.Fatalf("update README: %v", err)
	}
	runReleaseScriptTestCommand(t, repo, "git", "add", "README.md")
	runReleaseScriptTestCommand(t, repo, "git", "commit", "-m", "尚未推送")
	output = runReleaseScriptTestCommandExpectFailure(t, repo, "bash", "-c", checkCommand)
	if !strings.Contains(output, "origin/main") {
		t.Fatalf("diverged main rejection=%q, want origin/main hint", output)
	}
}

func TestReleaseScriptUpdateSmokeSkipsDryRun(t *testing.T) {
	script := releaseScriptPath(t)

	output := runReleaseScriptTestCommand(t, "", "bash", "-c", "WECLAW_RELEASE_SOURCE_ONLY=1 source "+shellQuote(script)+" && DRY_RUN=1 && verify_update_smoke && echo ok")
	if strings.TrimSpace(output) != "ok" {
		t.Fatalf("verify_update_smoke dry-run output=%q, want ok", output)
	}
}

func TestReleaseScriptUpdateSmokeSkipsUnsupportedHost(t *testing.T) {
	script := releaseScriptPath(t)
	command := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + ` && ` +
		`go() { if [[ "$1 $2" == "env GOHOSTOS" ]]; then echo windows; elif [[ "$1 $2" == "env GOHOSTARCH" ]]; then echo amd64; else echo unexpected-go-call >&2; return 9; fi; } && ` +
		`DRY_RUN=0 TAG=v9.9.9 verify_update_smoke`

	output := runReleaseScriptTestCommand(t, "", "bash", "-c", command)
	if !strings.Contains(output, "跳过 update smoke") {
		t.Fatalf("verify_update_smoke unsupported host output=%q, want skip hint", output)
	}
}

func TestReleaseWorkflowsBuildOfficialMatrix(t *testing.T) {
	requiredTargets := []string{
		"- goos: darwin\n            goarch: arm64",
		"- goos: darwin\n            goarch: amd64",
		"- goos: linux\n            goarch: arm64",
		"- goos: linux\n            goarch: amd64",
	}
	for _, path := range []string{
		filepath.Join("..", ".github", "workflows", "ci.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		for _, target := range requiredTargets {
			if !strings.Contains(text, target) {
				t.Fatalf("%s missing official release target %q", path, target)
			}
		}
		if strings.Contains(text, "goos: windows") {
			t.Fatalf("%s must not publish Windows assets", path)
		}
	}

	script, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, target := range []string{`"darwin/arm64"`, `"darwin/amd64"`, `"linux/arm64"`, `"linux/amd64"`} {
		if !strings.Contains(text, target) {
			t.Fatalf("release script missing official target %s", target)
		}
	}
	if !strings.Contains(text, "${#TARGETS[@]} + 1") {
		t.Fatal("release asset verification must derive expected count from TARGETS")
	}
	assertReleaseWorkflowDelegatesCanonicalScript(t)
}

func TestReleaseScriptVerifiesEveryOfficialAssetName(t *testing.T) {
	script := releaseScriptPath(t)
	fixture := validReleaseVerifyFixture()
	fixture.assets = strings.Join([]string{
		"weclaw_darwin_arm64",
		"weclaw_darwin_amd64",
		"weclaw_linux_arm64",
		"weclaw_linux_amd64",
		"checksums.txt",
	}, `\n`)
	command := releaseVerifyCommand(script, fixture)
	runReleaseScriptTestCommand(t, "", "bash", "-c", command)

	fixture.assets = strings.Replace(fixture.assets, "weclaw_linux_amd64", "unexpected_asset", 1)
	output := runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", releaseVerifyCommand(script, fixture))
	if !strings.Contains(output, "Release 缺少资产：weclaw_linux_amd64") {
		t.Fatalf("missing asset rejection=%q", output)
	}
}

func TestReleaseScriptRejectsDraftPrereleaseAndCorruptAsset(t *testing.T) {
	script := releaseScriptPath(t)
	tests := []struct {
		name   string
		mutate func(*releaseVerifyFixture)
		want   string
	}{
		{"draft", func(f *releaseVerifyFixture) { f.draft = true }, "draft"},
		{"prerelease", func(f *releaseVerifyFixture) { f.prerelease = true }, "prerelease"},
		{"corrupt checksum", func(f *releaseVerifyFixture) { f.corrupt = true }, "checksum"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validReleaseVerifyFixture()
			test.mutate(&fixture)
			output := runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", releaseVerifyCommand(script, fixture))
			if !strings.Contains(strings.ToLower(output), test.want) {
				t.Fatalf("rejection=%q, want %q", output, test.want)
			}
		})
	}
}

func TestReleaseScriptAcceptsDraftAssetsBeforePromotion(t *testing.T) {
	script := releaseScriptPath(t)
	fixture := validReleaseVerifyFixture()
	fixture.draft = true

	runReleaseScriptTestCommand(t, "", "bash", "-c", releaseVerifyCommandForInvocation(
		script, fixture, `DRY_RUN=0 TAG=v9.9.9 verify_release_assets true`,
	))

	output := runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", releaseVerifyCommand(script, fixture))
	if !strings.Contains(output, "draft") {
		t.Fatalf("final verification=%q, want draft rejection", output)
	}
}

func TestReleaseScriptStagesBeforeSmokeAndPromotion(t *testing.T) {
	content, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	ordered := []string{
		"stage_release\n", "verify_release_assets true\n", "verify_update_smoke\n",
		"promote_release\n", "verify_release\n", "RELEASE_COMMITTED=1\n",
	}
	previous := -1
	for _, marker := range ordered {
		index := strings.LastIndex(text, marker)
		if index <= previous {
			t.Fatalf("release order invalid at %q", marker)
		}
		previous = index
	}
	for _, required := range []string{"--draft", "--draft=false --latest", `WECLAW_UPDATE_RELEASE_TAG="$TAG"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("release transaction missing %q", required)
		}
	}
}

func TestReleaseScriptCleansDraftAndTagAfterFailure(t *testing.T) {
	script := releaseScriptPath(t)
	command := "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + ` && ` +
		`gh() { printf 'GH:%s\n' "$*"; } && ` +
		`git() { printf 'GIT:%s\n' "$*"; } && ` +
		`DRY_RUN=0 TAG=v9.9.9 RELEASE_TAG_CREATED=1 RELEASE_TAG_PUSHED=1 RELEASE_DRAFT_ATTEMPTED=1 RELEASE_COMMITTED=0 && ` +
		`set +e; false; cleanup_failed_release`

	output := runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", command)
	if !strings.Contains(output, "GH:release delete v9.9.9 --repo TingRuDeng/weclaw --cleanup-tag --yes") {
		t.Fatalf("cleanup output=%q, want draft release cleanup", output)
	}
}

func TestReleaseScriptAuthenticatesDraftUpdateSmoke(t *testing.T) {
	content, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{
		`RELEASE_DRAFT_ATTEMPTED=1`,
		`github_token="$(gh auth token)"`,
		`GITHUB_TOKEN="$github_token" WECLAW_HOME=`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("draft smoke transaction missing %q", required)
		}
	}
}

func TestReleaseValidationRunsGovulncheck(t *testing.T) {
	content, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatalf("read release script: %v", err)
	}
	if !strings.Contains(string(content), "govulncheck@v1.6.0") {
		t.Fatal("release validation must run pinned govulncheck v1.6.0")
	}
}

func TestReleaseValidationRunsTidyAndStaticcheck(t *testing.T) {
	const staticcheck = "honnef.co/go/tools/cmd/staticcheck@v0.7.0"
	for _, path := range []string{
		releaseScriptPath(t),
		filepath.Join("..", ".github", "workflows", "ci.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		if !strings.Contains(text, "go mod tidy -diff") || !strings.Contains(text, staticcheck) {
			t.Fatalf("%s must gate go.mod drift and pinned staticcheck", path)
		}
	}
	assertReleaseWorkflowDelegatesCanonicalScript(t)
}

func TestCIAndReleaseRunRepositoryHygieneGates(t *testing.T) {
	checks := map[string][]string{
		filepath.Join("..", ".github", "workflows", "ci.yml"): {
			"sh scripts/install_test.sh",
			"python3 scripts/validate_docs.py . --profile generic",
			"go vet ./...",
			"git diff --check",
		},
		releaseScriptPath(t): {
			`sh "$ROOT_DIR/scripts/install_test.sh"`,
			`python3 "$ROOT_DIR/scripts/validate_docs.py" . --profile generic`,
			"go vet ./...",
			"git diff --check",
		},
	}
	for path, required := range checks {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		for _, marker := range required {
			if !strings.Contains(text, marker) {
				t.Fatalf("%s missing repository hygiene gate %q", path, marker)
			}
		}
	}
}

func TestReleaseValidationRunsFullRepositoryRaceAndRejectsNoPackages(t *testing.T) {
	content, err := os.ReadFile(releaseScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{`packages="$(go list ./...)"`, `[[ -n "$packages" ]]`, "go test -race -count=1 -timeout 180s ./..."} {
		if !strings.Contains(text, required) {
			t.Fatalf("release validation missing %q", required)
		}
	}
	if strings.Contains(text, "./agent ./cmd ./messaging") {
		t.Fatal("release race gate must not use a scoped package list")
	}
}

func TestWorkflowsUseSecureGoToolchainAndVulnerabilityScan(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", ".github", "workflows", "ci.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		if !strings.Contains(text, "go-version: '1.26.5'") {
			t.Fatalf("%s must use Go 1.26.5", path)
		}
		if !strings.Contains(text, "govulncheck@v1.6.0") {
			t.Fatalf("%s must run pinned govulncheck v1.6.0", path)
		}
	}
	release, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(release), "go-version: '1.26.5'") {
		t.Fatal("stable release workflow must use Go 1.26.5")
	}
	assertReleaseWorkflowDelegatesCanonicalScript(t)
}

func TestReleaseWorkflowsPinThirdPartyReleaseAction(t *testing.T) {
	const pinnedReleaseAction = "softprops/action-gh-release@3d0d9888cb7fd7b750713d6e236d1fcb99157228 # v3.0.2"
	for _, path := range []string{
		filepath.Join("..", ".github", "workflows", "ci.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		if !strings.Contains(text, pinnedReleaseAction) {
			t.Fatalf("%s must pin softprops/action-gh-release to reviewed v3.0.2 commit", path)
		}
		if strings.Contains(text, "softprops/action-gh-release@v") {
			t.Fatalf("%s contains mutable softprops/action-gh-release tag", path)
		}
	}
	assertReleaseWorkflowDelegatesCanonicalScript(t)
}

func TestWorkflowsPinAllGitHubActionsToReviewedCommits(t *testing.T) {
	required := []string{
		"actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5.1.0",
		"actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6.5.0",
	}
	workflows := []string{
		filepath.Join("..", ".github", "workflows", "ci.yml"),
		filepath.Join("..", ".github", "workflows", "release.yml"),
		filepath.Join("..", ".github", "workflows", "mirror-gitee.yml"),
	}
	for _, path := range workflows {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		for _, action := range required {
			if !strings.Contains(text, action) {
				t.Fatalf("%s must pin reviewed action %q", path, action)
			}
		}
		for _, mutable := range []string{"actions/checkout@v", "actions/setup-go@v"} {
			if strings.Contains(text, mutable) {
				t.Fatalf("%s contains mutable action ref %q", path, mutable)
			}
		}
	}

	ci, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	ciText := string(ci)
	for _, action := range []string{
		"actions/upload-artifact@b7c566a772e6b6bfb58ed0dc250532a479d7789f # v6.0.0",
		"actions/download-artifact@37930b1c2abaa49bbe596cd826c3c89aef350131 # v7.0.0",
	} {
		if !strings.Contains(ciText, action) {
			t.Fatalf("CI workflow must pin reviewed action %q", action)
		}
	}
	for _, mutable := range []string{"actions/upload-artifact@v", "actions/download-artifact@v"} {
		if strings.Contains(ciText, mutable) {
			t.Fatalf("CI workflow contains mutable action ref %q", mutable)
		}
	}
}

func TestStableReleaseWorkflowIsManualOnlyAndBuildsRequestedTag(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "release.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取稳定版 workflow 失败：%v", err)
	}
	text := string(content)
	if strings.Contains(text, "\n  push:") {
		t.Fatal("稳定版 workflow 不应由 tag push 自动触发，本地发布脚本是唯一默认发布者")
	}
	if !strings.Contains(text, "workflow_dispatch:") {
		t.Fatal("稳定版 workflow 应保留手动兜底入口")
	}
	if !strings.Contains(text, "ref: main") {
		t.Fatal("手动发布必须 checkout main，由权威脚本校验 origin/main 后创建目标 tag")
	}
	for _, required := range []string{
		"fetch-depth: 0",
		`scripts/release.sh "$RELEASE_TAG"`,
		"GH_TOKEN: ${{ github.token }}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("手动发布缺少主分支来源校验 %q", required)
		}
	}
	if strings.Contains(text, "ref: ${{ inputs.tag }}") {
		t.Fatal("目标 tag 必须由 release.sh 在完成门禁后创建，不能预先 checkout")
	}
}

func assertReleaseWorkflowDelegatesCanonicalScript(t *testing.T) {
	t.Helper()
	path := filepath.Join("..", ".github", "workflows", "release.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, `scripts/release.sh "$RELEASE_TAG"`) {
		t.Fatal("stable release workflow must delegate to scripts/release.sh")
	}
	for _, duplicatedGate := range []string{"go test ./...", "govulncheck@", "softprops/action-gh-release@"} {
		if strings.Contains(text, duplicatedGate) {
			t.Fatalf("stable release workflow duplicates canonical release logic %q", duplicatedGate)
		}
	}
}

func TestPrereleaseWorkflowRecreatesMovingTagAtCurrentCommit(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "ci.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 CI workflow 失败：%v", err)
	}
	text := string(content)
	for _, required := range []string{
		"group: prerelease-${{ github.ref }}",
		"cancel-in-progress: true",
		"--cleanup-tag",
		"git/refs/tags/${RELEASE_TAG}",
		"target_commitish: ${{ github.sha }}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("预发布 moving tag 缺少 %q", required)
		}
	}
}

func TestPrereleaseWorkflowUsesCollisionResistantRefKey(t *testing.T) {
	path := filepath.Join("..", ".github", "workflows", "ci.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	text := string(content)
	for _, required := range []string{
		"REF_NAME: ${{ github.ref_name }}",
		"FULL_REF: ${{ github.ref }}",
		`SAFE_BRANCH=$(printf '%s' "$REF_NAME" | sed 's/[^a-zA-Z0-9._-]/-/g' | cut -c1-64)`,
		`BRANCH_HASH=$(printf '%s' "$FULL_REF" | sha256sum | cut -c1-12)`,
		`tag=alpha-${SAFE_BRANCH}-${BRANCH_HASH}`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("CI workflow missing collision-resistant prerelease key %q", required)
		}
	}
	if strings.Contains(text, `tag=alpha-${SAFE_BRANCH}"`) {
		t.Fatal("CI workflow still uses the lossy branch name as the complete prerelease tag")
	}
	for _, unsafe := range []string{
		`if [ "${{ github.ref_name }}"`,
		`echo "${{ github.ref_name }}" | sed`,
		`printf '%s' "${{ github.ref }}"`,
		`echo ${{ github.sha }} | cut`,
	} {
		if strings.Contains(text, unsafe) {
			t.Fatalf("CI workflow interpolates untrusted context inside shell: %q", unsafe)
		}
	}
}

func TestPrereleaseRefKeySeparatesNormalizedBranchCollisions(t *testing.T) {
	first := prereleaseRefKeyForTest("refs/heads/feature/a-b", "feature/a-b")
	second := prereleaseRefKeyForTest("refs/heads/feature-a-b", "feature-a-b")
	if first == second {
		t.Fatalf("normalized branch collision: %q", first)
	}
	for _, key := range []string{first, second} {
		if !strings.HasPrefix(key, "alpha-feature-a-b-") || len(key) > len("alpha-")+64+1+12 {
			t.Fatalf("unexpected prerelease key %q", key)
		}
	}
}

func prereleaseRefKeyForTest(fullRef string, refName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, refName)
	if len(safe) > 64 {
		safe = safe[:64]
	}
	sum := sha256.Sum256([]byte(fullRef))
	return fmt.Sprintf("alpha-%s-%x", safe, sum[:6])
}

func releaseScriptPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "scripts", "release.sh"))
	if err != nil {
		t.Fatalf("resolve release script: %v", err)
	}
	return abs
}

func runReleaseScriptTestCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runReleaseScriptTestCommandExpectFailure(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("%s %s unexpectedly succeeded\n%s", name, strings.Join(args, " "), output)
	}
	return string(output)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type releaseVerifyFixture struct {
	assets     string
	tag        string
	draft      bool
	prerelease bool
	corrupt    bool
}

func validReleaseVerifyFixture() releaseVerifyFixture {
	return releaseVerifyFixture{
		assets: strings.Join([]string{
			"weclaw_darwin_arm64",
			"weclaw_darwin_amd64",
			"weclaw_linux_arm64",
			"weclaw_linux_amd64",
			"checksums.txt",
		}, `\n`),
		tag: "v9.9.9",
	}
}

func releaseVerifyCommand(script string, fixture releaseVerifyFixture) string {
	return releaseVerifyCommandForInvocation(script, fixture, `DRY_RUN=0 TAG=v9.9.9 verify_release`)
}

func releaseVerifyCommandForInvocation(script string, fixture releaseVerifyFixture, invocation string) string {
	draft := "false"
	if fixture.draft {
		draft = "true"
	}
	prerelease := "false"
	if fixture.prerelease {
		prerelease = "true"
	}
	corrupt := "0"
	if fixture.corrupt {
		corrupt = "1"
	}
	return "WECLAW_RELEASE_SOURCE_ONLY=1 source " + shellQuote(script) + ` && ` +
		`FAKE_CORRUPT=` + corrupt + ` && ` +
		`gh() { ` +
		`if [[ "$1 $2" == "release download" ]]; then local dir=""; while (($#)); do if [[ "$1" == "--dir" ]]; then dir="$2"; shift 2; else shift; fi; done; mkdir -p "$dir"; ` +
		`for name in weclaw_darwin_arm64 weclaw_darwin_amd64 weclaw_linux_arm64 weclaw_linux_amd64; do printf '%s\n' "$name" > "$dir/$name"; done; ` +
		`(cd "$dir" && shasum -a 256 weclaw_* > checksums.txt); if [[ "$FAKE_CORRUPT" == 1 ]]; then printf 'corrupt\n' >> "$dir/weclaw_linux_amd64"; fi; return 0; fi; ` +
		`case "$*" in *".assets | length"*) echo 5 ;; *".assets[].name"*) printf '%b\n' "` + fixture.assets + `" ;; ` +
		`*"@tsv"*) printf '%s\t%s\t%s\n' "` + fixture.tag + `" "` + draft + `" "` + prerelease + `" ;; *"--json tagName"*) echo "` + fixture.tag + `" ;; esac; } && ` +
		invocation
}
