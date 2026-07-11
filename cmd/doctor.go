package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

type doctorStatus int

const (
	doctorOK doctorStatus = iota
	doctorWarn
	doctorFail
)

func (s doctorStatus) symbol() string {
	switch s {
	case doctorOK:
		return "[ok]"
	case doctorWarn:
		return "[warn]"
	default:
		return "[fail]"
	}
}

type doctorResult struct {
	Name   string
	Status doctorStatus
	Detail string
}

// doctorDeps 注入外部探测，便于单测无副作用地验证检查逻辑。
type doctorDeps struct {
	lookPath       func(string) (string, error)
	wechatAccounts func() (int, error)
	feishuCredsOK  func(string) error
	sudoProbe      func(user string) error
}

func defaultDoctorDeps() doctorDeps {
	return doctorDeps{
		lookPath: config.LookPath,
		wechatAccounts: func() (int, error) {
			accounts, err := ilink.LoadAllCredentials()
			return len(accounts), err
		},
		feishuCredsOK: func(name string) error {
			_, err := feishu.LoadCredentialsForBot(name)
			return err
		},
		sudoProbe: func(user string) error {
			return exec.Command("sudo", "-n", "-u", user, "true").Run()
		},
	}
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "检查配置、Agent 和平台状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		results := runDoctorChecks(cfg, defaultDoctorDeps())
		failed := 0
		for _, r := range results {
			fmt.Printf("%-7s %s%s\n", r.Status.symbol(), r.Name, detailSuffix(r.Detail))
			if r.Status == doctorFail {
				failed++
			}
		}
		if failed > 0 {
			return fmt.Errorf("doctor found %d blocking issue(s)", failed)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func detailSuffix(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	return " — " + detail
}

// runDoctorChecks 执行所有预检并返回结果列表，纯逻辑、可测试。
func runDoctorChecks(cfg *config.Config, deps doctorDeps) []doctorResult {
	var results []doctorResult
	results = append(results, checkAgents(cfg, deps)...)
	results = append(results, checkPlatforms(cfg, deps)...)
	results = append(results, checkAPIToken(cfg))
	results = append(results, checkWorkspaceRoots(cfg))
	results = append(results, checkAuditLog(cfg))
	return results
}

// checkAuditLog 校验审计日志目录可写（默认开启时）。
func checkAuditLog(cfg *config.Config) doctorResult {
	result := doctorResult{Name: "audit log"}
	if cfg.AuditLog != nil && !*cfg.AuditLog {
		result.Status = doctorWarn
		result.Detail = "audit log disabled; sensitive operations will not be recorded"
		return result
	}
	path := strings.TrimSpace(cfg.AuditLogPath)
	if path == "" {
		path = messaging.DefaultAuditLogPath()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		result.Status = doctorFail
		result.Detail = fmt.Sprintf("audit log dir not writable: %v", err)
		return result
	}
	result.Status = doctorOK
	result.Detail = path
	return result
}

// checkWorkspaceRoots 校验 /cwd 工作目录白名单：未配置时提示远程切换已禁用，配置项不存在时失败。
func checkWorkspaceRoots(cfg *config.Config) doctorResult {
	result := doctorResult{Name: "workspace confinement"}
	if len(cfg.AllowedWorkspaceRoots) == 0 {
		result.Status = doctorWarn
		result.Detail = "allowed_workspace_roots 未配置；普通用户远程 /cwd 切换已禁用，管理员不受此限制"
		return result
	}
	for _, root := range cfg.AllowedWorkspaceRoots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			result.Status = doctorFail
			result.Detail = fmt.Sprintf("allowed root not a directory: %s", root)
			return result
		}
	}
	result.Status = doctorOK
	result.Detail = fmt.Sprintf("%d root(s) configured", len(cfg.AllowedWorkspaceRoots))
	return result
}

func checkAgents(cfg *config.Config, deps doctorDeps) []doctorResult {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]doctorResult, 0, len(names))
	for _, name := range names {
		agCfg := cfg.Agents[name]
		results = append(results, checkAgentBinary(name, agCfg, deps))
		if strings.TrimSpace(agCfg.RunAsUser) != "" && (agCfg.Type == "cli" || agCfg.Type == "acp") {
			results = append(results, checkRunAsUser(name, agCfg.RunAsUser, deps))
		}
	}
	return results
}

func checkAgentBinary(name string, agCfg config.AgentConfig, deps doctorDeps) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("agent %q", name)}
	switch agCfg.Type {
	case "http":
		if strings.TrimSpace(agCfg.Endpoint) == "" {
			result.Status = doctorFail
			result.Detail = "http agent missing endpoint"
			return result
		}
		result.Status = doctorOK
		result.Detail = "endpoint " + agCfg.Endpoint
		return result
	case "cli", "acp", "companion":
		if strings.TrimSpace(agCfg.Command) == "" {
			result.Status = doctorFail
			result.Detail = "missing command"
			return result
		}
		if _, err := deps.lookPath(agCfg.Command); err != nil {
			result.Status = doctorFail
			result.Detail = fmt.Sprintf("command %q not found", agCfg.Command)
			return result
		}
		result.Status = doctorOK
		result.Detail = "command " + agCfg.Command
		return result
	default:
		result.Status = doctorWarn
		result.Detail = "unknown type " + agCfg.Type
		return result
	}
}

func checkRunAsUser(name string, user string, deps doctorDeps) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("agent %q run_as_user=%s", name, user)}
	if deps.sudoProbe == nil {
		result.Status = doctorWarn
		result.Detail = "sudo probe unavailable"
		return result
	}
	if err := deps.sudoProbe(user); err != nil {
		result.Status = doctorFail
		result.Detail = fmt.Sprintf("passwordless sudo to %q failed: %v", user, err)
		return result
	}
	result.Status = doctorOK
	result.Detail = "passwordless sudo verified"
	return result
}
