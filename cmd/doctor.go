package cmd

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
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
	feishuCredsOK  func() error
	sudoProbe      func(user string) error
}

func defaultDoctorDeps() doctorDeps {
	return doctorDeps{
		lookPath: config.LookPath,
		wechatAccounts: func() (int, error) {
			accounts, err := ilink.LoadAllCredentials()
			return len(accounts), err
		},
		feishuCredsOK: func() error {
			_, err := feishu.LoadCredentials()
			return err
		},
		sudoProbe: func(user string) error {
			return exec.Command("sudo", "-n", "-u", user, "true").Run()
		},
	}
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run pre-flight checks on config, agents, platforms and isolation",
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
	return results
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

func checkPlatforms(cfg *config.Config, deps doctorDeps) []doctorResult {
	var results []doctorResult
	wechatEnabled, feishuEnabled := platformEnablement(cfg)

	if wechatEnabled {
		results = append(results, checkWeChat(deps))
		results = append(results, checkAllowlist(cfg, string(platform.PlatformWeChat)))
	}
	if feishuEnabled {
		results = append(results, checkFeishu(deps))
		results = append(results, checkAllowlist(cfg, string(platform.PlatformFeishu)))
	}
	if !wechatEnabled && !feishuEnabled {
		results = append(results, doctorResult{Name: "platforms", Status: doctorWarn, Detail: "no platform enabled; nothing to run"})
	}
	return results
}

// platformEnablement 解析启用的平台，复用 start.go 语义：微信默认启用、飞书默认关闭。
func platformEnablement(cfg *config.Config) (wechat bool, feishu bool) {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	wechat = wechatCfg.Enabled == nil || *wechatCfg.Enabled
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	feishu = feishuCfg.Enabled != nil && *feishuCfg.Enabled
	return wechat, feishu
}

func checkWeChat(deps doctorDeps) doctorResult {
	result := doctorResult{Name: "platform wechat"}
	count, err := deps.wechatAccounts()
	if err != nil {
		result.Status = doctorFail
		result.Detail = "load credentials: " + err.Error()
		return result
	}
	if count == 0 {
		result.Status = doctorFail
		result.Detail = "no WeChat account; run `weclaw login`"
		return result
	}
	result.Status = doctorOK
	result.Detail = fmt.Sprintf("%d account(s)", count)
	return result
}

func checkFeishu(deps doctorDeps) doctorResult {
	result := doctorResult{Name: "platform feishu"}
	if err := deps.feishuCredsOK(); err != nil {
		result.Status = doctorFail
		result.Detail = err.Error()
		return result
	}
	result.Status = doctorOK
	result.Detail = "credentials present"
	return result
}

func checkAllowlist(cfg *config.Config, name string) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("access control %s", name)}
	pc, ok := cfg.Platforms[name]
	if !ok || len(pc.AllowedUsers) == 0 {
		result.Status = doctorWarn
		result.Detail = "empty allow_users → default-deny rejects everyone; add allowed_users"
		return result
	}
	result.Status = doctorOK
	result.Detail = fmt.Sprintf("%d allowed user(s)", len(pc.AllowedUsers))
	return result
}

func checkAPIToken(cfg *config.Config) doctorResult {
	result := doctorResult{Name: "api server"}
	addr := strings.TrimSpace(cfg.APIAddr)
	if addr == "" || isLoopbackAddr(addr) {
		result.Status = doctorOK
		result.Detail = "loopback or default address"
		return result
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		result.Status = doctorFail
		result.Detail = fmt.Sprintf("api_addr %q is non-loopback but api_token is empty", addr)
		return result
	}
	result.Status = doctorOK
	result.Detail = "token configured for non-loopback address"
	return result
}

func isLoopbackAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
