package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

const doctorDependencyProbeTimeout = 10 * time.Second

type doctorComponent string

const (
	componentSQLite     doctorComponent = "sqlite3"
	componentBubblewrap doctorComponent = "bubblewrap"
	componentNodeJS     doctorComponent = "nodejs"
	componentNPM        doctorComponent = "npm"
	componentCodex      doctorComponent = "codex"
	componentClaude     doctorComponent = "claude"
	componentClaudeACP  doctorComponent = "claude-acp"
)

var doctorComponentOrder = []doctorComponent{
	componentSQLite,
	componentBubblewrap,
	componentNodeJS,
	componentNPM,
	componentCodex,
	componentClaude,
	componentClaudeACP,
}

type doctorInstallCommand struct {
	Name string
	Args []string
	Env  []string
}

type doctorInstallPlanRequest struct {
	GOOS               string
	PackageManager     string
	Root               bool
	NPMPrefix          string
	CodexInstallerPath string
	CodexInstallDir    string
	CodexHome          string
	Components         []doctorComponent
}

func checkDoctorDependencies(cfg *config.Config, deps doctorDeps) []doctorResult {
	results := make([]doctorResult, 0, 8)
	codexConfigured := configuredAgent(cfg, "codex")
	claudeConfigured := configuredAgent(cfg, "claude")

	codexCommand, nativeCodexRequired := configuredNativeCodexCommand(cfg)
	codexPath, codexErr := lookupDoctorDependency(deps, codexCommand)
	results = append(results, checkCodexCapabilities(nativeCodexRequired, codexPath, codexErr, deps))
	results = append(results, checkCodexStandalone(cfg, deps))
	results = append(results, checkCodexSessionCatalog(codexConfigured || codexErr == nil, cfg, deps))
	if deps.goos == "linux" && (nativeCodexRequired || (codexCommand == "codex" && codexErr == nil)) {
		results = append(results, checkSimpleDependency(deps, "Codex sandbox", "bwrap", doctorWarn,
			"缺少 bubblewrap；Codex 将使用 bundled helper，运行 weclaw doctor --fix 安装"))
	}

	claudePath, claudeErr := lookupDoctorDependency(deps, "claude")
	results = append(results, checkOptionalCommand("Claude Code CLI", claudePath, claudeErr))
	if !claudeConfigured {
		adapterPath, adapterErr := lookupDoctorDependency(deps, "claude-agent-acp")
		results = append(results, checkOptionalCommand("Claude ACP adapter", adapterPath, adapterErr))
	}

	results = append(results, checkNodeRuntime(deps, claudeConfigured))
	results = append(results, checkNPM(deps, claudeConfigured))
	return results
}

func checkCodexStandalone(cfg *config.Config, deps doctorDeps) doctorResult {
	result := doctorResult{Name: "Codex standalone daemon"}
	if cfg == nil {
		result.Status = doctorOK
		result.Detail = "当前配置不要求官方 daemon"
		return result
	}
	agentConfig, configured := cfg.Agents["codex"]
	if !configured || !isCodexAppServerAgent(agentConfig) {
		result.Status = doctorOK
		result.Detail = "当前配置不要求官方 daemon"
		return result
	}
	mode := agentConfig.EffectiveCodexHostMode()
	if mode == "managed" {
		result.Status = doctorOK
		result.Detail = "当前显式使用 managed 兼容 Host"
		return result
	}
	codexHome := ""
	if deps.codexHome != nil {
		codexHome = strings.TrimSpace(deps.codexHome(cfg))
	}
	path := filepath.Join(codexHome, "packages", "standalone", "current", "codex")
	info, err := os.Stat(path)
	if codexHome != "" && err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		result.Status = doctorOK
		result.Detail = path
		return result
	}
	result.Status = doctorWarn
	result.Detail = "未安装官方 standalone；auto 将使用 managed 兼容 Host，受控 weclaw codex cli 不可用"
	if mode == "daemon" {
		result.Status = doctorFail
		result.Detail = "codex_host_mode=daemon 需要官方 standalone；运行 weclaw doctor --fix --components codex"
	}
	return result
}

func configuredNativeCodexCommand(cfg *config.Config) (string, bool) {
	if cfg == nil {
		return "codex", false
	}
	agentCfg, ok := cfg.Agents["codex"]
	if !ok || strings.ToLower(strings.TrimSpace(agentCfg.Type)) != "acp" {
		return "codex", false
	}
	command := strings.TrimSpace(agentCfg.Command)
	base := strings.ToLower(filepath.Base(command))
	if base == "codex" || base == "codex.exe" {
		return command, true
	}
	return "codex", false
}

func configuredAgent(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	_, ok := cfg.Agents[name]
	return ok
}

func lookupDoctorDependency(deps doctorDeps, name string) (string, error) {
	if deps.lookPath == nil {
		return "", fmt.Errorf("command lookup unavailable")
	}
	return deps.lookPath(name)
}

func checkCodexCapabilities(configured bool, path string, lookupErr error, deps doctorDeps) doctorResult {
	result := doctorResult{Name: "Codex CLI capabilities"}
	if lookupErr != nil {
		if configured {
			result.Status = doctorFail
			result.Detail = "找不到 codex；运行 weclaw doctor --fix 安装"
		} else {
			result.Status = doctorWarn
			result.Detail = "未安装（可选）；运行 weclaw doctor --fix 安装"
		}
		return result
	}
	if deps.commandOutput == nil {
		result.Status = doctorFail
		result.Detail = "app-server capability probe unavailable"
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), doctorDependencyProbeTimeout)
	defer cancel()
	if _, err := deps.commandOutput(ctx, path, "app-server", "--help"); err != nil {
		if configured {
			result.Status = doctorFail
		} else {
			result.Status = doctorWarn
		}
		result.Detail = fmt.Sprintf("codex app-server 不可用: %v", err)
		return result
	}
	result.Status = doctorOK
	result.Detail = path + "; app-server verified"
	return result
}

func checkCodexSessionCatalog(relevant bool, cfg *config.Config, deps doctorDeps) doctorResult {
	result := doctorResult{Name: "Codex session catalog"}
	if !relevant {
		result.Status = doctorOK
		result.Detail = "Codex 未启用，sqlite3 暂不需要"
		return result
	}
	sqlitePath, err := lookupDoctorDependency(deps, "sqlite3")
	if err != nil {
		result.Status = doctorWarn
		result.Detail = "缺少 sqlite3，/cx 会话目录不可用；运行 weclaw doctor --fix 安装"
		return result
	}
	codexHome := ""
	if deps.codexHome != nil {
		codexHome = strings.TrimSpace(deps.codexHome(cfg))
	}
	dbPath := filepath.Join(codexHome, "state_5.sqlite")
	if codexHome == "" {
		result.Status = doctorOK
		result.Detail = sqlitePath
		return result
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			result.Status = doctorOK
			result.Detail = sqlitePath + "; Codex 状态库尚未创建"
			return result
		}
		result.Status = doctorWarn
		result.Detail = fmt.Sprintf("无法检查 Codex 状态库: %v", err)
		return result
	}
	if deps.commandOutput == nil {
		result.Status = doctorWarn
		result.Detail = "sqlite3 probe unavailable"
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), doctorDependencyProbeTimeout)
	defer cancel()
	output, err := deps.commandOutput(ctx, sqlitePath, "-readonly", dbPath, "pragma quick_check;")
	if err != nil || strings.TrimSpace(output) != "ok" {
		result.Status = doctorWarn
		detail := strings.TrimSpace(output)
		if err != nil {
			if detail != "" {
				detail += "; "
			}
			detail += err.Error()
		}
		if detail == "" {
			detail = "未返回 quick_check=ok"
		}
		result.Detail = "Codex 状态库只读检查失败: " + detail
		return result
	}
	result.Status = doctorOK
	result.Detail = sqlitePath + "; state_5.sqlite quick_check=ok"
	return result
}

func defaultDoctorCodexHome(cfg *config.Config) string {
	if cfg != nil {
		if agentCfg, ok := cfg.Agents["codex"]; ok {
			if value := strings.TrimSpace(agentCfg.Env["CODEX_HOME"]); value != "" {
				return value
			}
		}
	}
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func checkSimpleDependency(deps doctorDeps, resultName, command string, missingStatus doctorStatus, missingDetail string) doctorResult {
	result := doctorResult{Name: resultName}
	path, err := lookupDoctorDependency(deps, command)
	if err != nil {
		result.Status = missingStatus
		result.Detail = missingDetail
		return result
	}
	result.Status = doctorOK
	result.Detail = path
	return result
}

func checkOptionalCommand(name, path string, err error) doctorResult {
	if err != nil {
		return doctorResult{Name: name, Status: doctorWarn, Detail: "未安装（可选）；运行 weclaw doctor --fix 安装"}
	}
	return doctorResult{Name: name, Status: doctorOK, Detail: path}
}

func checkNodeRuntime(deps doctorDeps, blocking bool) doctorResult {
	result := doctorResult{Name: "Node.js runtime"}
	path, err := lookupDoctorDependency(deps, "node")
	if err != nil {
		result.Status = doctorWarn
		if blocking {
			result.Status = doctorFail
		}
		result.Detail = "缺少 Node.js 22+；Claude/ACP 不可用"
		return result
	}
	if deps.commandOutput == nil {
		result.Status = doctorWarn
		if blocking {
			result.Status = doctorFail
		}
		result.Detail = "Node.js version probe unavailable"
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), doctorDependencyProbeTimeout)
	defer cancel()
	output, err := deps.commandOutput(ctx, path, "--version")
	if err != nil || parseNodeMajor(output) < 22 {
		result.Status = doctorWarn
		if blocking {
			result.Status = doctorFail
		}
		result.Detail = fmt.Sprintf("需要 Node.js 22+，当前为 %q", strings.TrimSpace(output))
		return result
	}
	result.Status = doctorOK
	result.Detail = strings.TrimSpace(output) + " at " + path
	return result
}

func checkNPM(deps doctorDeps, blocking bool) doctorResult {
	result := checkSimpleDependency(deps, "npm", "npm", doctorWarn, "缺少 npm；无法安装 Claude/ACP")
	if blocking && result.Status != doctorOK {
		result.Status = doctorFail
	}
	if result.Status != doctorOK || deps.commandOutput == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), doctorDependencyProbeTimeout)
	defer cancel()
	version, err := deps.commandOutput(ctx, result.Detail, "--version")
	if err != nil {
		result.Status = doctorWarn
		if blocking {
			result.Status = doctorFail
		}
		result.Detail = fmt.Sprintf("npm version probe failed: %v", err)
		return result
	}
	result.Detail = strings.TrimSpace(version) + " at " + result.Detail
	return result
}

func parseNodeMajor(version string) int {
	version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	majorText := strings.SplitN(version, ".", 2)[0]
	major, _ := strconv.Atoi(majorText)
	return major
}

func expandDoctorComponents(requested []doctorComponent) ([]doctorComponent, error) {
	selected := make(map[doctorComponent]bool)
	for _, component := range requested {
		switch component {
		case componentSQLite, componentBubblewrap, componentNodeJS, componentNPM:
			selected[component] = true
		case componentCodex:
			selected[componentCodex] = true
		case componentClaude, componentClaudeACP:
			selected[componentNodeJS] = true
			selected[componentNPM] = true
			selected[componentClaude] = true
			selected[componentClaudeACP] = true
		default:
			return nil, fmt.Errorf("未知依赖组件 %q", component)
		}
	}
	expanded := make([]doctorComponent, 0, len(selected))
	for _, component := range doctorComponentOrder {
		if selected[component] {
			expanded = append(expanded, component)
		}
	}
	return expanded, nil
}

func buildDoctorInstallPlan(req doctorInstallPlanRequest) ([]doctorInstallCommand, error) {
	packages := make([]string, 0, len(req.Components))
	seenPackage := make(map[string]bool)
	appendPackage := func(name string) {
		if name != "" && !seenPackage[name] {
			seenPackage[name] = true
			packages = append(packages, name)
		}
	}
	npmPackages := make([]string, 0, 2)
	installCodex := false
	for _, component := range req.Components {
		switch component {
		case componentSQLite:
			if req.GOOS == "darwin" {
				appendPackage("sqlite")
			} else if req.PackageManager == "dnf" {
				appendPackage("sqlite")
			} else {
				appendPackage("sqlite3")
			}
		case componentBubblewrap:
			if req.GOOS != "linux" {
				return nil, fmt.Errorf("bubblewrap 只支持 Linux")
			}
			appendPackage("bubblewrap")
		case componentNodeJS, componentNPM:
			if req.GOOS == "darwin" {
				appendPackage("node")
			} else {
				if component == componentNodeJS {
					appendPackage("nodejs")
				} else {
					appendPackage("npm")
				}
			}
		case componentCodex:
			installCodex = true
		case componentClaude:
			npmPackages = append(npmPackages, "@anthropic-ai/claude-code")
		case componentClaudeACP:
			npmPackages = append(npmPackages, "@agentclientprotocol/claude-agent-acp@0.58.1")
		default:
			return nil, fmt.Errorf("未知依赖组件 %q", component)
		}
	}
	plan := make([]doctorInstallCommand, 0, len(npmPackages)+4)
	if len(packages) > 0 {
		switch {
		case req.GOOS == "darwin" && req.PackageManager == "brew":
			plan = append(plan, doctorInstallCommand{Name: "brew", Args: append([]string{"install"}, packages...)})
		case req.GOOS == "linux" && req.PackageManager == "apt-get":
			plan = appendPrivilegedInstall(plan, req.Root, "apt-get", []string{"update"})
			plan = appendPrivilegedInstall(plan, req.Root, "apt-get", append([]string{"install", "-y"}, packages...))
		case req.GOOS == "linux" && req.PackageManager == "dnf":
			plan = appendPrivilegedInstall(plan, req.Root, "dnf", append([]string{"install", "-y"}, packages...))
		default:
			return nil, fmt.Errorf("不支持的包管理器 %q（%s）", req.PackageManager, req.GOOS)
		}
	}
	if installCodex {
		installerPath := strings.TrimSpace(req.CodexInstallerPath)
		if installerPath == "" || !filepath.IsAbs(installerPath) {
			return nil, fmt.Errorf("Codex standalone 安装器需要绝对临时路径")
		}
		installDir := strings.TrimSpace(req.CodexInstallDir)
		if installDir == "" || !filepath.IsAbs(installDir) {
			return nil, fmt.Errorf("Codex standalone 安装目录必须是绝对路径")
		}
		plan = append(plan, doctorInstallCommand{
			Name: "curl", Args: []string{"-fsSL", "https://chatgpt.com/codex/install.sh", "-o", installerPath},
		})
		installerEnv := []string{"CODEX_NON_INTERACTIVE=1", "CODEX_INSTALL_DIR=" + installDir}
		if codexHome := strings.TrimSpace(req.CodexHome); codexHome != "" {
			if !filepath.IsAbs(codexHome) {
				return nil, fmt.Errorf("CODEX_HOME 必须是绝对路径")
			}
			installerEnv = append(installerEnv, "CODEX_HOME="+filepath.Clean(codexHome))
		}
		plan = append(plan, doctorInstallCommand{Name: "sh", Args: []string{installerPath}, Env: installerEnv})
	}
	for _, packageName := range npmPackages {
		args := []string{"install", "--global"}
		if strings.TrimSpace(req.NPMPrefix) != "" {
			args = append(args, "--prefix", req.NPMPrefix)
		}
		plan = append(plan, doctorInstallCommand{Name: "npm", Args: append(args, packageName)})
	}
	return plan, nil
}

func appendPrivilegedInstall(plan []doctorInstallCommand, root bool, command string, args []string) []doctorInstallCommand {
	if root {
		return append(plan, doctorInstallCommand{Name: command, Args: args})
	}
	return append(plan, doctorInstallCommand{Name: "sudo", Args: append([]string{command}, args...)})
}

func validateDoctorFixRequest(interactive, yes bool, components []doctorComponent) error {
	if !interactive && (!yes || len(components) == 0) {
		return fmt.Errorf("非交互环境必须同时指定 --components 和 --yes")
	}
	return nil
}
