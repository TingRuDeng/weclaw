package cmd

import (
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
		`DRY_RUN=0 TAG=v9.9.9 verify_release`
}
