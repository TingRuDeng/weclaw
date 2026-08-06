package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestDoctorDependenciesWarnBeforeCodexCatalogNeedsSQLite(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	deps := testDoctorDeps()
	deps.goos = "linux"
	deps.codexHome = func(*config.Config) string { return t.TempDir() }
	deps.lookPath = func(name string) (string, error) {
		switch name {
		case "sqlite3":
			return "", fmt.Errorf("not found")
		case "codex", "bwrap", "node", "npm":
			return "/usr/bin/" + name, nil
		default:
			return "", fmt.Errorf("not found")
		}
	}
	deps.commandOutput = func(context.Context, string, ...string) (string, error) {
		return "v22.18.0", nil
	}

	result, ok := findResult(checkDoctorDependencies(cfg, deps), "Codex session catalog")
	if !ok {
		t.Fatal("missing Codex session catalog dependency result")
	}
	if result.Status != doctorWarn || !containsAll(result.Detail, "sqlite3", "/cx") {
		t.Fatalf("result=%#v, want sqlite3 warning with feature impact", result)
	}
}

func TestDoctorDependenciesPreserveSQLiteQuickCheckFailure(t *testing.T) {
	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	deps := testDoctorDeps()
	deps.codexHome = func(*config.Config) string { return codexHome }
	deps.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	deps.commandOutput = func(_ context.Context, command string, args ...string) (string, error) {
		if strings.HasSuffix(command, "/sqlite3") {
			return "database disk image is malformed", nil
		}
		return "v22.18.0", nil
	}

	result, ok := findResult(checkDoctorDependencies(cfg, deps), "Codex session catalog")
	if !ok || result.Status != doctorWarn || !strings.Contains(result.Detail, "database disk image is malformed") {
		t.Fatalf("result=%#v ok=%t, want preserved quick_check output", result, ok)
	}
}

func TestDoctorDependenciesFailConfiguredCodexWithoutAppServer(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	deps := testDoctorDeps()
	deps.goos = "darwin"
	deps.lookPath = func(name string) (string, error) {
		if name == "codex" || name == "sqlite3" {
			return "/usr/local/bin/" + name, nil
		}
		return "", fmt.Errorf("not found")
	}
	deps.commandOutput = func(_ context.Context, command string, args ...string) (string, error) {
		if strings.HasSuffix(command, "/codex") && strings.Join(args, " ") == "app-server --help" {
			return "", fmt.Errorf("unknown command app-server")
		}
		return "", nil
	}

	result, ok := findResult(checkDoctorDependencies(cfg, deps), "Codex CLI capabilities")
	if !ok {
		t.Fatal("missing Codex CLI capability result")
	}
	if result.Status != doctorFail || !strings.Contains(result.Detail, "app-server") {
		t.Fatalf("result=%#v, want blocking app-server failure", result)
	}
}

func TestDoctorDependenciesDoNotBlockConfiguredCodexACPAdapterWithoutCodexCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex-acp"}
	deps := testDoctorDeps()
	deps.goos = "linux"
	deps.lookPath = func(name string) (string, error) {
		if name == "codex-acp" {
			return "/usr/local/bin/codex-acp", nil
		}
		return "", fmt.Errorf("not found")
	}

	result, ok := findResult(checkDoctorDependencies(cfg, deps), "Codex CLI capabilities")
	if !ok {
		t.Fatal("missing Codex CLI capability result")
	}
	if result.Status == doctorFail {
		t.Fatalf("optional Codex CLI must not block a configured codex-acp adapter: %#v", result)
	}
}

func TestDoctorDependenciesReportNodeAndNPMBeforeAgentSelection(t *testing.T) {
	cfg := config.DefaultConfig()
	deps := testDoctorDeps()
	deps.lookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }

	results := checkDoctorDependencies(cfg, deps)
	node, nodeOK := findResult(results, "Node.js runtime")
	npm, npmOK := findResult(results, "npm")
	if !nodeOK || !npmOK {
		t.Fatalf("missing Node/npm results: node=%#v nodeOK=%t npm=%#v npmOK=%t", node, nodeOK, npm, npmOK)
	}
	if node.Status != doctorWarn || npm.Status != doctorWarn {
		t.Fatalf("Node/npm should be optional warnings before Agent selection: node=%#v npm=%#v", node, npm)
	}
}

func TestExpandDoctorComponentsAddsAgentInstallPrerequisites(t *testing.T) {
	got, err := expandDoctorComponents([]doctorComponent{componentClaude})
	if err != nil {
		t.Fatal(err)
	}
	want := []doctorComponent{componentNodeJS, componentNPM, componentClaude, componentClaudeACP}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("expanded components=%v, want %v", got, want)
	}
}

func TestBuildDoctorInstallPlanForDebianUsesFixedArguments(t *testing.T) {
	plan, err := buildDoctorInstallPlan(doctorInstallPlanRequest{
		GOOS: "linux", PackageManager: "apt-get", Root: false,
		Components: []doctorComponent{
			componentSQLite, componentBubblewrap, componentNodeJS, componentNPM, componentCodex,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []doctorInstallCommand{
		{Name: "sudo", Args: []string{"apt-get", "update"}},
		{Name: "sudo", Args: []string{"apt-get", "install", "-y", "sqlite3", "bubblewrap", "nodejs", "npm"}},
		{Name: "npm", Args: []string{"install", "--global", "@openai/codex"}},
	}
	if fmt.Sprint(plan) != fmt.Sprint(want) {
		t.Fatalf("install plan=%v, want %v", plan, want)
	}
	for _, command := range plan {
		if command.Name == "sh" || command.Name == "bash" {
			t.Fatalf("install plan must not invoke a shell: %#v", command)
		}
	}
}

func TestBuildDoctorInstallPlanUsesUserPrefixForNPM(t *testing.T) {
	plan, err := buildDoctorInstallPlan(doctorInstallPlanRequest{
		GOOS: "linux", PackageManager: "", Root: false, NPMPrefix: "/home/debian/.local",
		Components: []doctorComponent{componentClaude, componentClaudeACP},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []doctorInstallCommand{
		{Name: "npm", Args: []string{"install", "--global", "--prefix", "/home/debian/.local", "@anthropic-ai/claude-code"}},
		{Name: "npm", Args: []string{"install", "--global", "--prefix", "/home/debian/.local", "@agentclientprotocol/claude-agent-acp@0.58.1"}},
	}
	if fmt.Sprint(plan) != fmt.Sprint(want) {
		t.Fatalf("install plan=%v, want user-prefix npm commands %v", plan, want)
	}
}

func TestPromptDoctorComponentsExplainsDependencyRoles(t *testing.T) {
	var output bytes.Buffer
	selected, cancelled, err := promptDoctorComponents(
		bufio.NewReader(strings.NewReader("\n")),
		&output,
		[]doctorComponent{componentSQLite, componentNodeJS, componentClaude},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled || len(selected) != 0 {
		t.Fatalf("selected=%v cancelled=%t, want cancellation", selected, cancelled)
	}
	text := output.String()
	if !containsAll(text,
		"[可选增强] sqlite3",
		"[安装前置] nodejs",
		"[可选 Agent] claude",
		"自动包含 Node.js 22+、npm 和 Claude ACP",
	) {
		t.Fatalf("prompt=%q, want dependency roles and linked prerequisites", text)
	}
}

func TestDoctorPromptOmitsLinuxOnlyBubblewrapOnDarwin(t *testing.T) {
	deps := doctorFixDeps{
		GOOS: "darwin",
		LookPath: func(string) (string, error) {
			return "", fmt.Errorf("not found")
		},
	}
	available := doctorComponentsAvailableForPrompt(deps)
	if containsDoctorComponent(available, componentBubblewrap) {
		t.Fatalf("darwin prompt must not offer Linux-only bubblewrap: %v", available)
	}
}

func TestValidateDoctorFixRequestRejectsNonInteractiveImplicitInstall(t *testing.T) {
	err := validateDoctorFixRequest(false, false, nil)
	if err == nil || !containsAll(err.Error(), "非交互", "--components", "--yes") {
		t.Fatalf("error=%v, want explicit non-interactive guard", err)
	}
}

func TestRunDoctorFixInstallsSelectedComponentAndRechecks(t *testing.T) {
	installed := false
	deps := doctorFixDeps{
		GOOS: "linux", Root: false,
		LookPath: func(name string) (string, error) {
			switch name {
			case "sqlite3":
				if installed {
					return "/usr/bin/sqlite3", nil
				}
				return "", fmt.Errorf("not found")
			case "apt-get", "sudo":
				return "/usr/bin/" + name, nil
			default:
				return "", fmt.Errorf("not found")
			}
		},
		RunCommand: func(_ context.Context, command doctorInstallCommand, _ io.Reader, _, _ io.Writer) error {
			if command.Name == "sudo" && containsAll(strings.Join(command.Args, " "), "apt-get", "install", "sqlite3") {
				installed = true
			}
			return nil
		},
	}
	var output bytes.Buffer
	err := runDoctorFix(context.Background(), doctorFixOptions{
		Components:  []doctorComponent{componentSQLite},
		Yes:         true,
		Interactive: false,
		Input:       strings.NewReader(""),
		Output:      &output,
		ErrorOutput: &output,
		Deps:        deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !installed || !containsAll(output.String(), "sqlite3", "验证通过") {
		t.Fatalf("installed=%t output=%q", installed, output.String())
	}
}

func TestRunDoctorFixUsesUserNPMPrefixAndMakesItDiscoverable(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	var commands []doctorInstallCommand
	configured := false
	deps := doctorFixDeps{
		GOOS: "linux", Root: false,
		UserHomeDir: func() (string, error) { return home, nil },
		LookPath: func(name string) (string, error) {
			switch name {
			case "node", "npm":
				return "/usr/bin/" + name, nil
			default:
				return exec.LookPath(name)
			}
		},
		CommandOutput: func(_ context.Context, command string, _ ...string) (string, error) {
			if strings.HasSuffix(command, "/node") {
				return "v22.23.2", nil
			}
			return "", nil
		},
		RunCommand: func(_ context.Context, command doctorInstallCommand, _ io.Reader, _, _ io.Writer) error {
			commands = append(commands, command)
			name := "claude"
			if strings.Contains(strings.Join(command.Args, " "), "claude-agent-acp") {
				name = "claude-agent-acp"
			}
			return os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700)
		},
		Configure: func(context.Context, *config.Config) error {
			configured = true
			if _, err := exec.LookPath("claude-agent-acp"); err != nil {
				return fmt.Errorf("user-prefix adapter not discoverable: %w", err)
			}
			return nil
		},
	}
	var output bytes.Buffer
	err := runDoctorFix(context.Background(), doctorFixOptions{
		Components: []doctorComponent{componentClaude}, Yes: true, Interactive: false,
		Input: strings.NewReader(""), Output: &output, ErrorOutput: &output,
		Config: config.DefaultConfig(), Deps: deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !configured || len(commands) != 2 {
		t.Fatalf("configured=%t commands=%v", configured, commands)
	}
	for _, command := range commands {
		joined := strings.Join(command.Args, " ")
		if !containsAll(joined, "--prefix", filepath.Join(home, ".local")) {
			t.Fatalf("npm command lacks user prefix: %#v", command)
		}
		if command.Name == "sudo" {
			t.Fatalf("npm install must not use sudo: %#v", command)
		}
	}
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("PATH=%q after doctor --fix, want original value restored", got)
	}
}

func TestRunDoctorFixDoesNotPrependUserBinBeforePrivilegedCommands(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const originalPath = "/usr/bin:/bin"
	t.Setenv("PATH", originalPath)
	systemInstalled := false
	var privilegedPath string
	deps := doctorFixDeps{
		GOOS: "linux", Root: false,
		UserHomeDir: func() (string, error) { return home, nil },
		LookPath: func(name string) (string, error) {
			switch name {
			case "apt-get", "sudo":
				return "/usr/bin/" + name, nil
			case "node", "npm":
				if systemInstalled {
					return "/usr/bin/" + name, nil
				}
				return "", fmt.Errorf("not found")
			case "claude", "claude-agent-acp":
				return exec.LookPath(name)
			default:
				return "", fmt.Errorf("not found")
			}
		},
		CommandOutput: func(_ context.Context, command string, _ ...string) (string, error) {
			if strings.HasSuffix(command, "/node") {
				return "v22.23.2", nil
			}
			return "", nil
		},
		RunCommand: func(_ context.Context, command doctorInstallCommand, _ io.Reader, _, _ io.Writer) error {
			joined := strings.Join(command.Args, " ")
			if command.Name == "sudo" && strings.Contains(joined, "apt-get install") {
				privilegedPath = os.Getenv("PATH")
				systemInstalled = true
				return nil
			}
			if strings.HasSuffix(command.Name, "/npm") {
				name := "claude"
				if strings.Contains(joined, "claude-agent-acp") {
					name = "claude-agent-acp"
				}
				return os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700)
			}
			return nil
		},
	}
	err := runDoctorFix(context.Background(), doctorFixOptions{
		Components: []doctorComponent{componentClaude}, Yes: true, Interactive: false,
		Input: strings.NewReader(""), Output: io.Discard, ErrorOutput: io.Discard,
		Config: config.DefaultConfig(), Deps: deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if privilegedPath != originalPath {
		t.Fatalf("privileged command PATH=%q, want trusted original PATH %q", privilegedPath, originalPath)
	}
}

func TestRunDoctorFixFailsWhenPostInstallProbeStillMissing(t *testing.T) {
	deps := doctorFixDeps{
		GOOS: "linux", Root: true,
		LookPath: func(name string) (string, error) {
			if name == "apt-get" {
				return "/usr/bin/apt-get", nil
			}
			return "", fmt.Errorf("not found")
		},
		RunCommand: func(context.Context, doctorInstallCommand, io.Reader, io.Writer, io.Writer) error {
			return nil
		},
	}
	err := runDoctorFix(context.Background(), doctorFixOptions{
		Components:  []doctorComponent{componentSQLite},
		Yes:         true,
		Interactive: false,
		Input:       strings.NewReader(""),
		Output:      io.Discard,
		ErrorOutput: io.Discard,
		Deps:        deps,
	})
	if err == nil || !containsAll(err.Error(), "sqlite3", "仍不可用") {
		t.Fatalf("error=%v, want post-install verification failure", err)
	}
}

func TestRunDoctorFixInteractiveDeclineDoesNotInstall(t *testing.T) {
	installed := false
	deps := doctorFixDeps{
		GOOS: "linux", Root: true,
		LookPath: func(name string) (string, error) {
			if name == "apt-get" {
				return "/usr/bin/apt-get", nil
			}
			return "", fmt.Errorf("not found")
		},
		RunCommand: func(context.Context, doctorInstallCommand, io.Reader, io.Writer, io.Writer) error {
			installed = true
			return nil
		},
	}
	var output bytes.Buffer
	err := runDoctorFix(context.Background(), doctorFixOptions{
		Components:  []doctorComponent{componentSQLite},
		Interactive: true,
		Input:       strings.NewReader("n\n"),
		Output:      &output,
		ErrorOutput: &output,
		Deps:        deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if installed || !strings.Contains(output.String(), "已取消") {
		t.Fatalf("installed=%t output=%q", installed, output.String())
	}
}

func TestParseDoctorComponentsRejectsUnknownName(t *testing.T) {
	_, err := parseDoctorComponents("sqlite3,anything")
	if err == nil || !strings.Contains(err.Error(), "anything") {
		t.Fatalf("error=%v, want unknown component rejection", err)
	}
}

func TestDoctorNodeRequirementDependsOnSelectedAgent(t *testing.T) {
	deps := doctorFixDeps{
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		CommandOutput: func(_ context.Context, command string, args ...string) (string, error) {
			if strings.HasSuffix(command, "/node") {
				return "v20.19.0", nil
			}
			return "", nil
		},
	}
	codex, err := expandDoctorComponents([]doctorComponent{componentCodex})
	if err != nil {
		t.Fatal(err)
	}
	if got := doctorComponentsNeedingInstall(codex, deps); containsDoctorComponent(got, componentNodeJS) {
		t.Fatalf("Codex install should accept Node.js 20, got missing=%v", got)
	}
	claude, err := expandDoctorComponents([]doctorComponent{componentClaude})
	if err != nil {
		t.Fatal(err)
	}
	if got := doctorComponentsNeedingInstall(claude, deps); !containsDoctorComponent(got, componentNodeJS) {
		t.Fatalf("Claude ACP requires Node.js 22+, got missing=%v", got)
	}
}

func TestDoctorFixCapabilityProbeHasDeadline(t *testing.T) {
	sawDeadline := false
	deps := doctorFixDeps{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		CommandOutput: func(ctx context.Context, _ string, _ ...string) (string, error) {
			_, sawDeadline = ctx.Deadline()
			return "", nil
		},
	}
	_ = doctorComponentsNeedingInstall([]doctorComponent{componentCodex}, deps)
	if !sawDeadline {
		t.Fatal("doctor --fix capability probes must have a deadline")
	}
}

func containsDoctorComponent(components []doctorComponent, target doctorComponent) bool {
	for _, component := range components {
		if component == target {
			return true
		}
	}
	return false
}
