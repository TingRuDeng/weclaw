package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

const (
	defaultConfigPermissionAgent = "codex"
	levelDefault                 = "default"
	levelAutoReview              = "auto_review"
	levelFullAccess              = "full_access"
)

var (
	configPermissionAgent string
	configPermissionLevel string
)

type configPermissionOptions struct {
	Agent    string
	AgentSet bool
	Level    string
	LevelSet bool
}

type configPermissionPrompter interface {
	Prompt(label string, defaultValue string) (string, error)
}

var configPermissionCmd = &cobra.Command{
	Use:   "permission",
	Short: "配置 Codex 权限档位",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigPermission(configPermissionOptions{
			Agent:    configPermissionAgent,
			AgentSet: cmd.Flags().Changed("agent"),
			Level:    configPermissionLevel,
			LevelSet: cmd.Flags().Changed("level"),
		}, newTerminalConfigPrompter(os.Stdin, os.Stdout))
	},
}

// init 注册权限档位命令参数，支持脚本化配置和本机交互配置。
func init() {
	configPermissionCmd.Flags().StringVar(&configPermissionAgent, "agent", defaultConfigPermissionAgent, "要配置的 Agent 名称")
	configPermissionCmd.Flags().StringVar(&configPermissionLevel, "level", "", "Codex 权限档位：default、auto_review、full_access")
}

// runConfigPermission 读取用户输入并写入 Codex 权限档位。
func runConfigPermission(opts configPermissionOptions, prompter configPermissionPrompter) error {
	if prompter == nil {
		return fmt.Errorf("配置交互输入器不能为空")
	}
	next, err := collectConfigPermissionOptions(opts, prompter)
	if err != nil {
		return err
	}
	return applyCodexPermissionLevel(next)
}

// collectConfigPermissionOptions 补齐命令行未提供的选项，支持本机交互配置。
func collectConfigPermissionOptions(opts configPermissionOptions, prompter configPermissionPrompter) (configPermissionOptions, error) {
	var err error
	opts.Agent = strings.TrimSpace(opts.Agent)
	if opts.Agent == "" {
		opts.Agent = defaultConfigPermissionAgent
	}
	if !opts.AgentSet && !opts.LevelSet {
		if opts.Agent, err = prompter.Prompt("Agent", opts.Agent); err != nil {
			return configPermissionOptions{}, err
		}
	}
	opts.Agent = strings.TrimSpace(opts.Agent)
	if opts.Agent == "" {
		return configPermissionOptions{}, fmt.Errorf("agent 不能为空")
	}
	if !opts.LevelSet {
		opts.Level, err = prompter.Prompt("权限档位 default/auto_review/full_access", levelDefault)
		if err != nil {
			return configPermissionOptions{}, err
		}
	}
	opts.Level = normalizePermissionLevelInput(opts.Level)
	if err := validatePermissionLevel(opts.Level); err != nil {
		return configPermissionOptions{}, err
	}
	return opts, nil
}

// applyCodexPermissionLevel 写入权限档位，并清理会覆盖档位映射的高级字段。
func applyCodexPermissionLevel(opts configPermissionOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	agentCfg, ok := cfg.Agents[opts.Agent]
	if !ok {
		return fmt.Errorf("agent %q 不存在，请先配置该 agent", opts.Agent)
	}
	agentCfg.PermissionLevel = opts.Level
	agentCfg.ApprovalPolicy = ""
	agentCfg.ApprovalReviewer = ""
	agentCfg.SandboxMode = ""
	cfg.Agents[opts.Agent] = agentCfg
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	printConfigPermissionResult(opts)
	return nil
}

// normalizePermissionLevelInput 兼容连字符输入，最终统一写入下划线档位名。
func normalizePermissionLevelInput(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	return strings.ReplaceAll(level, "-", "_")
}

// validatePermissionLevel 只允许官方支持的三档，避免写入启动后才失败的值。
func validatePermissionLevel(level string) error {
	switch level {
	case levelDefault, levelAutoReview, levelFullAccess:
		return nil
	default:
		return fmt.Errorf("无效权限档位 %q，请使用 default、auto_review 或 full_access", level)
	}
}

// printConfigPermissionResult 输出实际写入结果和重启提示，避免用户误以为运行中 Agent 已热更新。
func printConfigPermissionResult(opts configPermissionOptions) {
	fmt.Printf("已更新 %s 权限档位：%s\n", opts.Agent, opts.Level)
	fmt.Printf("映射：%s\n", permissionLevelSummary(opts.Level))
	fmt.Println("已清空 approval_policy / approval_reviewer / sandbox_mode 高级覆盖。")
	if path, err := config.ConfigPath(); err == nil {
		fmt.Printf("已写入：%s\n", path)
	}
	fmt.Println("如 WeClaw 正在运行，请重启后让已启动的 Codex Agent 使用新权限档位。")
}

// permissionLevelSummary 返回用户可读的 Codex 参数映射，便于确认安全边界。
func permissionLevelSummary(level string) string {
	switch level {
	case levelDefault:
		return "workspace-write + on-request + user reviewer"
	case levelAutoReview:
		return "workspace-write + on-request + auto_review reviewer"
	case levelFullAccess:
		return "danger-full-access + never"
	default:
		return "未知映射"
	}
}

type terminalConfigPrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

// newTerminalConfigPrompter 创建无第三方依赖的终端输入器，便于测试中替换。
func newTerminalConfigPrompter(in io.Reader, out io.Writer) *terminalConfigPrompter {
	return &terminalConfigPrompter{
		reader: bufio.NewReader(in),
		out:    out,
	}
}

// Prompt 输出普通文本问题；空答案会采用默认值。
func (p *terminalConfigPrompter) Prompt(label string, defaultValue string) (string, error) {
	prompt := label
	if defaultValue != "" {
		prompt = fmt.Sprintf("%s [%s]", label, defaultValue)
	}
	if _, err := fmt.Fprintf(p.out, "%s: ", prompt); err != nil {
		return "", err
	}
	value, err := p.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}
