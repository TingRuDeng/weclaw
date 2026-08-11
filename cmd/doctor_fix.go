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
	SaveConfig    func(*config.Config) error
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
			cmd.Env = mergeDoctorCommandEnv(os.Environ(), command.Env)
			cmd.Stdin = in
			cmd.Stdout = out
			cmd.Stderr = errOut
			return cmd.Run()
		},
		Configure:   configureDoctorInstalledAgents,
		SaveConfig:  saveDoctorPinnedCodexConfig,
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
		missing := doctorComponentsAvailableForPrompt(opts.Deps, opts.Config)
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
	needed := doctorComponentsNeedingInstallForFix(expanded, opts.Deps, opts.Config)
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
	codexInstallDir, err := doctorCodexInstallDir(opts.Deps, needed)
	if err != nil {
		return err
	}
	codexInstallerPath := ""
	if hasDoctorComponent(needed, componentCodex) {
		installerDir, err := os.MkdirTemp("", "weclaw-codex-installer-")
		if err != nil {
			return fmt.Errorf("创建 Codex 安装器临时目录失败: %w", err)
		}
		codexInstallerPath = filepath.Join(installerDir, "install.sh")
		defer func() {
			_ = os.Remove(codexInstallerPath)
			_ = os.Remove(installerDir)
		}()
	}
	plan, err := buildDoctorInstallPlan(doctorInstallPlanRequest{
		GOOS: opts.Deps.GOOS, PackageManager: manager, Root: opts.Deps.Root,
		NPMPrefix: npmPrefix, CodexInstallerPath: codexInstallerPath,
		CodexInstallDir: codexInstallDir, CodexHome: configuredDoctorCodexHomeOverride(opts.Config),
		Components: needed,
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
	// 用户可写目录只用于发现刚安装的 Agent，不能参与 sudo 或包管理器本身的解析。
	binDirs := []string{codexInstallDir}
	if npmPrefix != "" {
		binDirs = append(binDirs, filepath.Join(npmPrefix, "bin"))
	}
	restorePath, err := prependDoctorAgentBinsToPath(binDirs)
	if err != nil {
		return err
	}
	defer restorePath()
	remaining := doctorComponentsNeedingInstallForFix(needed, opts.Deps, opts.Config)
	if len(remaining) > 0 {
		return fmt.Errorf("安装命令已完成，但 %s 仍不可用", joinDoctorComponents(remaining))
	}
	if opts.Deps.Configure != nil && installsAgentComponent(needed) {
		if err := opts.Deps.Configure(ctx, opts.Config); err != nil {
			return fmt.Errorf("安装完成但 Agent 配置失败: %w", err)
		}
	}
	pinned, err := pinDoctorStandaloneCodexCommand(opts.Config, codexInstallDir, needed)
	if err != nil {
		return err
	}
	if pinned {
		if opts.Deps.SaveConfig == nil {
			return fmt.Errorf("安装完成但 Codex 绝对路径无法保存")
		}
		if err := opts.Deps.SaveConfig(opts.Config); err != nil {
			return fmt.Errorf("安装完成但保存 Codex 绝对路径失败: %w", err)
		}
	}
	fmt.Fprintf(opts.Output, "%s 安装后验证通过。\n", joinDoctorComponents(needed))
	return nil
}

func doctorComponentsAvailableForPrompt(deps doctorFixDeps, cfg *config.Config) []doctorComponent {
	supported := make([]doctorComponent, 0, len(doctorComponentOrder))
	for _, component := range doctorComponentOrder {
		if component == componentBubblewrap && deps.GOOS != "linux" {
			continue
		}
		supported = append(supported, component)
	}
	return doctorComponentsNeedingInstallForFix(supported, deps, cfg)
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
		return "[安装前置] nodejs — Claude npm 安装运行时"
	case componentNPM:
		return "[安装前置] npm — Claude 官方包安装器"
	case componentCodex:
		return "[可选 Agent] codex — 使用 OpenAI 官方 standalone 安装器"
	case componentClaude:
		return "[可选 Agent] claude — 自动包含 Node.js 22+、npm 和 Claude ACP"
	case componentClaudeACP:
		return "[Claude 必需] claude-acp — 选择 Claude 后自动加入固定版本 adapter"
	default:
		return string(component)
	}
}

func doctorNPMInstallPrefix(deps doctorFixDeps, components []doctorComponent) (string, error) {
	if deps.Root || !installsNPMComponent(components) {
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

func doctorCodexInstallDir(deps doctorFixDeps, components []doctorComponent) (string, error) {
	if !hasDoctorComponent(components, componentCodex) {
		return "", nil
	}
	if deps.UserHomeDir == nil {
		return "", fmt.Errorf("安装 Codex standalone 需要可用的用户主目录")
	}
	home, err := deps.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("读取用户主目录失败: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("Codex standalone 安装目录必须基于绝对用户主目录")
	}
	return filepath.Join(home, ".local", "bin"), nil
}

func prependDoctorAgentBinsToPath(binDirs []string) (func(), error) {
	prefixes := make([]string, 0, len(binDirs))
	seen := make(map[string]bool)
	for _, binDir := range binDirs {
		binDir = strings.TrimSpace(binDir)
		if binDir == "" {
			continue
		}
		if !filepath.IsAbs(binDir) {
			return nil, fmt.Errorf("Agent 安装目录必须是绝对路径")
		}
		binDir = filepath.Clean(binDir)
		if !seen[binDir] {
			seen[binDir] = true
			prefixes = append(prefixes, binDir)
		}
	}
	if len(prefixes) == 0 {
		return func() {}, nil
	}
	original, existed := os.LookupEnv("PATH")
	updated := strings.Join(prefixes, string(os.PathListSeparator))
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

func doctorComponentsNeedingInstallForFix(components []doctorComponent, deps doctorFixDeps, cfg *config.Config) []doctorComponent {
	missing := doctorComponentsNeedingInstall(components, deps)
	missingSet := make(map[doctorComponent]bool, len(missing))
	for _, component := range missing {
		missingSet[component] = true
	}
	if hasDoctorComponent(components, componentCodex) && !doctorStandaloneCodexAvailable(cfg) {
		missingSet[componentCodex] = true
	}
	result := make([]doctorComponent, 0, len(missingSet))
	for _, component := range components {
		if missingSet[component] {
			result = append(result, component)
		}
	}
	return result
}

func doctorStandaloneCodexAvailable(cfg *config.Config) bool {
	codexHome := strings.TrimSpace(defaultDoctorCodexHome(cfg))
	if codexHome == "" || !filepath.IsAbs(codexHome) {
		return false
	}
	path := filepath.Join(codexHome, "packages", "standalone", "current", "codex")
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func pinDoctorStandaloneCodexCommand(cfg *config.Config, installDir string, components []doctorComponent) (bool, error) {
	if cfg == nil || !hasDoctorComponent(components, componentCodex) {
		return false, nil
	}
	agentConfig, ok := cfg.Agents["codex"]
	if !ok || !isCodexAppServerAgent(agentConfig) {
		return false, nil
	}
	command := filepath.Join(strings.TrimSpace(installDir), "codex")
	info, err := os.Stat(command)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		if err == nil {
			err = fmt.Errorf("不是可执行普通文件")
		}
		return false, fmt.Errorf("安装完成但 Codex 可见命令无效 %s: %w", command, err)
	}
	command = filepath.Clean(command)
	if filepath.Clean(agentConfig.Command) == command {
		return false, nil
	}
	agentConfig.Command = command
	cfg.Agents["codex"] = agentConfig
	return true, nil
}

func configuredDoctorCodexHomeOverride(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	agentConfig, ok := cfg.Agents["codex"]
	if !ok {
		return ""
	}
	return strings.TrimSpace(agentConfig.Env["CODEX_HOME"])
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
	for _, component := range components {
		switch component {
		case componentClaude, componentClaudeACP:
			return 22
		}
	}
	return 22
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
	var committed *config.Config
	if err := config.Update(func(latest *config.Config) error {
		config.DetectAndConfigure(latest)
		if claudeCfg, ok := latest.Agents["claude"]; ok {
			if err := defaultClaudeACPProbe(ctx, "claude", claudeCfg); err != nil {
				return fmt.Errorf("Claude ACP 能力预检失败: %w", err)
			}
		}
		if err := latest.ValidateClaudeACPAgents(); err != nil {
			return err
		}
		committed = latest
		return nil
	}); err != nil {
		return err
	}
	if committed != nil {
		*cfg = *committed
	}
	return nil
}

func saveDoctorPinnedCodexConfig(candidate *config.Config) error {
	if candidate == nil {
		return nil
	}
	codexCfg, ok := candidate.Agents["codex"]
	if !ok {
		return nil
	}
	return config.Update(func(latest *config.Config) error {
		if latest.Agents == nil {
			latest.Agents = make(map[string]config.AgentConfig)
		}
		latestCodex, exists := latest.Agents["codex"]
		if !exists {
			latestCodex = codexCfg
		} else {
			latestCodex.Command = codexCfg.Command
		}
		latest.Agents["codex"] = latestCodex
		return latest.ValidateClaudeACPAgents()
	})
}

func installsAgentComponent(components []doctorComponent) bool {
	for _, component := range components {
		if component == componentCodex || component == componentClaude || component == componentClaudeACP {
			return true
		}
	}
	return false
}

func installsNPMComponent(components []doctorComponent) bool {
	for _, component := range components {
		if component == componentNPM || component == componentClaude || component == componentClaudeACP {
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
	parts := make([]string, 0, len(command.Env)+len(command.Args)+1)
	parts = append(parts, command.Env...)
	parts = append(parts, command.Name)
	for _, arg := range command.Args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			parts = append(parts, strconv.Quote(arg))
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}

func hasDoctorComponent(components []doctorComponent, target doctorComponent) bool {
	for _, component := range components {
		if component == target {
			return true
		}
	}
	return false
}

func mergeDoctorCommandEnv(base []string, overrides []string) []string {
	result := append([]string(nil), base...)
	positions := make(map[string]int, len(result))
	for index, item := range result {
		if split := strings.IndexByte(item, '='); split > 0 {
			positions[item[:split]] = index
		}
	}
	for _, item := range overrides {
		split := strings.IndexByte(item, '=')
		if split <= 0 {
			continue
		}
		key := item[:split]
		if index, ok := positions[key]; ok {
			result[index] = item
			continue
		}
		positions[key] = len(result)
		result = append(result, item)
	}
	return result
}
