package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

type doctorFixDeps struct {
	GOOS          string
	Root          bool
	LookPath      func(string) (string, error)
	CommandOutput func(context.Context, string, ...string) (string, error)
	RunCommand    func(context.Context, doctorInstallCommand, io.Reader, io.Writer, io.Writer) error
	Configure     func(context.Context, *config.Config) error
	UserHomeDir   func() (string, error)
}

type doctorFixOptions struct {
	Components  []doctorComponent
	Yes         bool
	Interactive bool
	Input       io.Reader
	Output      io.Writer
	ErrorOutput io.Writer
	Config      *config.Config
	Deps        doctorFixDeps
}

func defaultDoctorFixDeps() doctorFixDeps {
	return doctorFixDeps{
		GOOS:     runtime.GOOS,
		Root:     os.Geteuid() == 0,
		LookPath: config.LookPath,
		CommandOutput: func(ctx context.Context, command string, args ...string) (string, error) {
			output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
			return strings.TrimSpace(string(output)), err
		},
		RunCommand: func(ctx context.Context, command doctorInstallCommand, in io.Reader, out, errOut io.Writer) error {
			cmd := exec.CommandContext(ctx, command.Name, command.Args...)
			cmd.Stdin = in
			cmd.Stdout = out
			cmd.Stderr = errOut
			return cmd.Run()
		},
		Configure:   configureDoctorInstalledAgents,
		UserHomeDir: os.UserHomeDir,
	}
}

func parseDoctorComponents(raw string) ([]doctorComponent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	components := make([]doctorComponent, 0)
	for _, item := range strings.Split(raw, ",") {
		component := doctorComponent(strings.ToLower(strings.TrimSpace(item)))
		if !knownDoctorComponent(component) {
			return nil, fmt.Errorf("未知依赖组件 %q；可选值：%s", item, doctorComponentNames())
		}
		components = append(components, component)
	}
	return components, nil
}

func knownDoctorComponent(component doctorComponent) bool {
	for _, known := range doctorComponentOrder {
		if component == known {
			return true
		}
	}
	return false
}

func doctorComponentNames() string {
	names := make([]string, 0, len(doctorComponentOrder))
	for _, component := range doctorComponentOrder {
		names = append(names, string(component))
	}
	return strings.Join(names, ",")
}

func runDoctorFix(ctx context.Context, opts doctorFixOptions) error {
	if opts.Input == nil {
		opts.Input = strings.NewReader("")
	}
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	if opts.ErrorOutput == nil {
		opts.ErrorOutput = io.Discard
	}
	if err := validateDoctorFixRequest(opts.Interactive, opts.Yes, opts.Components); err != nil {
		return err
	}
	reader := bufio.NewReader(opts.Input)
	requested := opts.Components
	if len(requested) == 0 {
		missing := doctorComponentsAvailableForPrompt(opts.Deps)
		selected, cancelled, err := promptDoctorComponents(reader, opts.Output, missing)
		if err != nil {
			return err
		}
		if cancelled {
			fmt.Fprintln(opts.Output, "已取消依赖安装。")
			return nil
		}
		requested = selected
	}
	expanded, err := expandDoctorComponents(requested)
	if err != nil {
		return err
	}
	needed := doctorComponentsNeedingInstall(expanded, opts.Deps)
	if len(needed) == 0 {
		fmt.Fprintln(opts.Output, "所选依赖已经可用，无需安装。")
		return nil
	}
	manager, err := detectDoctorPackageManager(opts.Deps, needed)
	if err != nil {
		return err
	}
	npmPrefix, err := doctorNPMInstallPrefix(opts.Deps, needed)
	if err != nil {
		return err
	}
	plan, err := buildDoctorInstallPlan(doctorInstallPlanRequest{
		GOOS: opts.Deps.GOOS, PackageManager: manager, Root: opts.Deps.Root,
		NPMPrefix: npmPrefix, Components: needed,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Output, "将安装：%s\n", joinDoctorComponents(needed))
	for _, command := range plan {
		fmt.Fprintf(opts.Output, "  %s\n", formatDoctorInstallCommand(command))
	}
	if !opts.Yes {
		confirmed, err := promptDoctorConfirmation(reader, opts.Output)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(opts.Output, "已取消依赖安装。")
			return nil
		}
	}
	if opts.Deps.RunCommand == nil {
		return fmt.Errorf("依赖安装执行器不可用")
	}
	if err := executeDoctorInstallPlan(ctx, plan, opts, needed); err != nil {
		return err
	}
	// 用户可写目录只用于发现刚安装的 Agent，不能参与 sudo、包管理器或 npm 本身的解析。
	restorePath, err := prependDoctorNPMBinToPath(npmPrefix)
	if err != nil {
		return err
	}
	defer restorePath()
	remaining := doctorComponentsNeedingInstall(needed, opts.Deps)
	if len(remaining) > 0 {
		return fmt.Errorf("安装命令已完成，但 %s 仍不可用", joinDoctorComponents(remaining))
	}
	if opts.Deps.Configure != nil && installsAgentComponent(needed) {
		if err := opts.Deps.Configure(ctx, opts.Config); err != nil {
			return fmt.Errorf("安装完成但 Agent 配置失败: %w", err)
		}
	}
	fmt.Fprintf(opts.Output, "%s 安装后验证通过。\n", joinDoctorComponents(needed))
	return nil
}

func doctorComponentsAvailableForPrompt(deps doctorFixDeps) []doctorComponent {
	supported := make([]doctorComponent, 0, len(doctorComponentOrder))
	for _, component := range doctorComponentOrder {
		if component == componentBubblewrap && deps.GOOS != "linux" {
			continue
		}
		supported = append(supported, component)
	}
	return doctorComponentsNeedingInstall(supported, deps)
}

func promptDoctorComponents(reader *bufio.Reader, out io.Writer, available []doctorComponent) ([]doctorComponent, bool, error) {
	if len(available) == 0 {
		fmt.Fprintln(out, "未发现可自动修复的缺失依赖。")
		return nil, true, nil
	}
	fmt.Fprintln(out, "请选择需要安装的组件（逗号分隔编号，直接回车取消）：")
	for index, component := range available {
		fmt.Fprintf(out, "  [%d] %s\n", index+1, doctorComponentPromptLabel(component))
	}
	fmt.Fprint(out, "> ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, true, nil
	}
	selected := make([]doctorComponent, 0)
	seen := make(map[doctorComponent]bool)
	for _, token := range strings.Split(line, ",") {
		index, err := strconv.Atoi(strings.TrimSpace(token))
		if err != nil || index < 1 || index > len(available) {
			return nil, false, fmt.Errorf("无效组件编号 %q", token)
		}
		component := available[index-1]
		if !seen[component] {
			seen[component] = true
			selected = append(selected, component)
		}
	}
	return selected, false, nil
}

func doctorComponentPromptLabel(component doctorComponent) string {
	switch component {
	case componentSQLite:
		return "[可选增强] sqlite3 — Codex 会话目录与状态库检查"
	case componentBubblewrap:
		return "[可选增强] bubblewrap — Linux Codex 沙箱"
	case componentNodeJS:
		return "[安装前置] nodejs — Codex/Claude npm 安装运行时"
	case componentNPM:
		return "[安装前置] npm — Codex/Claude 官方包安装器"
	case componentCodex:
		return "[可选 Agent] codex — 自动包含 Node.js 和 npm"
	case componentClaude:
		return "[可选 Agent] claude — 自动包含 Node.js 22+、npm 和 Claude ACP"
	case componentClaudeACP:
		return "[Claude 必需] claude-acp — 选择 Claude 后自动加入固定版本 adapter"
	default:
		return string(component)
	}
}

func doctorNPMInstallPrefix(deps doctorFixDeps, components []doctorComponent) (string, error) {
	if deps.Root || !installsAgentComponent(components) {
		return "", nil
	}
	if deps.UserHomeDir == nil {
		return "", fmt.Errorf("普通用户安装 npm Agent 需要可用的用户主目录")
	}
	home, err := deps.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("读取用户主目录失败: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("普通用户 npm 安装目录必须基于绝对用户主目录")
	}
	return filepath.Join(home, ".local"), nil
}

func prependDoctorNPMBinToPath(prefix string) (func(), error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return func() {}, nil
	}
	binDir := filepath.Join(prefix, "bin")
	original, existed := os.LookupEnv("PATH")
	updated := binDir
	if original != "" {
		updated += string(os.PathListSeparator) + original
	}
	if err := os.Setenv("PATH", updated); err != nil {
		return nil, fmt.Errorf("设置用户级 npm PATH 失败: %w", err)
	}
	return func() {
		if existed {
			_ = os.Setenv("PATH", original)
		} else {
			_ = os.Unsetenv("PATH")
		}
	}, nil
}

func promptDoctorConfirmation(reader *bufio.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "将执行以上系统或官方安装命令，是否继续？[y/N] ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "确认":
		return true, nil
	default:
		return false, nil
	}
}

func doctorComponentsNeedingInstall(components []doctorComponent, deps doctorFixDeps) []doctorComponent {
	needed := make([]doctorComponent, 0, len(components))
	nodeMinimum := doctorRequiredNodeMajor(components)
	for _, component := range components {
		if doctorComponentNeedsInstall(component, nodeMinimum, deps) {
			needed = append(needed, component)
		}
	}
	return needed
}

func doctorComponentNeedsInstall(component doctorComponent, nodeMinimum int, deps doctorFixDeps) bool {
	command := map[doctorComponent]string{
		componentSQLite: "sqlite3", componentBubblewrap: "bwrap", componentNodeJS: "node",
		componentNPM: "npm", componentCodex: "codex", componentClaude: "claude",
		componentClaudeACP: "claude-agent-acp",
	}[component]
	if deps.LookPath == nil {
		return true
	}
	path, err := deps.LookPath(command)
	if err != nil {
		return true
	}
	if component == componentNodeJS {
		if deps.CommandOutput == nil {
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), doctorDependencyProbeTimeout)
		defer cancel()
		output, err := deps.CommandOutput(ctx, path, "--version")
		return err != nil || parseNodeMajor(output) < nodeMinimum
	}
	if component == componentCodex {
		if deps.CommandOutput == nil {
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), doctorDependencyProbeTimeout)
		defer cancel()
		_, err := deps.CommandOutput(ctx, path, "app-server", "--help")
		return err != nil
	}
	return false
}

func doctorRequiredNodeMajor(components []doctorComponent) int {
	minimum := 22
	hasCodex := false
	for _, component := range components {
		switch component {
		case componentClaude, componentClaudeACP:
			return 22
		case componentCodex:
			hasCodex = true
		}
	}
	if hasCodex {
		minimum = 16
	}
	return minimum
}

func detectDoctorPackageManager(deps doctorFixDeps, components []doctorComponent) (string, error) {
	needsSystemPackages := false
	for _, component := range components {
		switch component {
		case componentSQLite, componentBubblewrap, componentNodeJS, componentNPM:
			needsSystemPackages = true
		}
	}
	if !needsSystemPackages {
		return "", nil
	}
	candidates := []string{"apt-get", "dnf"}
	if deps.GOOS == "darwin" {
		candidates = []string{"brew"}
	}
	for _, candidate := range candidates {
		if _, err := deps.LookPath(candidate); err == nil {
			if !deps.Root && deps.GOOS == "linux" {
				if _, err := deps.LookPath("sudo"); err != nil {
					return "", fmt.Errorf("安装系统依赖需要 sudo，但当前 PATH 中找不到 sudo")
				}
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到受支持的系统包管理器")
}

func executeDoctorInstallPlan(ctx context.Context, plan []doctorInstallCommand, opts doctorFixOptions, components []doctorComponent) error {
	nodeMinimum := doctorRequiredNodeMajor(components)
	for _, planned := range plan {
		command := planned
		if command.Name == "npm" {
			nodePath, err := opts.Deps.LookPath("node")
			if err != nil || opts.Deps.CommandOutput == nil {
				return fmt.Errorf("运行 npm 前未找到可用的 Node.js %d+", nodeMinimum)
			}
			probeCtx, cancel := context.WithTimeout(ctx, doctorDependencyProbeTimeout)
			version, versionErr := opts.Deps.CommandOutput(probeCtx, nodePath, "--version")
			cancel()
			if versionErr != nil || parseNodeMajor(version) < nodeMinimum {
				return fmt.Errorf("运行 npm 前需要 Node.js %d+，当前为 %q", nodeMinimum, strings.TrimSpace(version))
			}
			npmPath, err := opts.Deps.LookPath("npm")
			if err != nil {
				return fmt.Errorf("运行 npm 安装前仍找不到 npm: %w", err)
			}
			command.Name = npmPath
		}
		fmt.Fprintf(opts.Output, "正在执行：%s\n", formatDoctorInstallCommand(planned))
		if err := opts.Deps.RunCommand(ctx, command, opts.Input, opts.Output, opts.ErrorOutput); err != nil {
			return fmt.Errorf("安装 %s 失败（%s）: %w", joinDoctorComponents(components), formatDoctorInstallCommand(planned), err)
		}
	}
	return nil
}

func configureDoctorInstalledAgents(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	if !config.DetectAndConfigure(cfg) {
		return nil
	}
	if claudeCfg, ok := cfg.Agents["claude"]; ok {
		if err := defaultClaudeACPProbe(ctx, "claude", claudeCfg); err != nil {
			return fmt.Errorf("Claude ACP 能力预检失败: %w", err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.ValidateClaudeACPAgents(); err != nil {
		return err
	}
	return config.Save(cfg)
}

func installsAgentComponent(components []doctorComponent) bool {
	for _, component := range components {
		if component == componentCodex || component == componentClaude || component == componentClaudeACP {
			return true
		}
	}
	return false
}

func joinDoctorComponents(components []doctorComponent) string {
	names := make([]string, 0, len(components))
	for _, component := range components {
		names = append(names, string(component))
	}
	return strings.Join(names, ", ")
}

func formatDoctorInstallCommand(command doctorInstallCommand) string {
	parts := []string{command.Name}
	for _, arg := range command.Args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			parts = append(parts, strconv.Quote(arg))
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}
