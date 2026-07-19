package cmd

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

const defaultConfigAgentName = "claude"

var (
	configAgentName         string
	configAgentCommand      string
	configAgentLocalCommand string
)

type configAgentLookPath func(string) (string, error)
type configAgentProbe func(string, config.AgentConfig) error

type configAgentOptions struct {
	Name         string
	Command      string
	LocalCommand string
	LookPath     configAgentLookPath
	Probe        configAgentProbe
}

var configAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "配置 ACP Agent 及可选本地辅助命令",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigAgent(configAgentOptions{
			Name: configAgentName, Command: configAgentCommand,
			LocalCommand: configAgentLocalCommand, LookPath: config.LookPath,
			Probe: func(name string, agentCfg config.AgentConfig) error {
				return defaultClaudeACPProbe(cmd.Context(), name, agentCfg)
			},
		})
	},
}

// init 注册 ACP Agent 配置命令；Claude 旧 CLI 配置通过该入口显式迁移。
func init() {
	configAgentCmd.Flags().StringVar(&configAgentName, "name", defaultConfigAgentName, "Agent 名称")
	configAgentCmd.Flags().StringVar(&configAgentCommand, "command", "", "ACP adapter 命令，Claude 默认自动查找 claude-agent-acp")
	configAgentCmd.Flags().StringVar(&configAgentLocalCommand, "local-command", "", "本地辅助命令，Claude 仅用于额度查询回退并默认查找 claude")
}

// runConfigAgent 读取旧配置并原地迁移，保留与 CLI 启动方式无关的业务字段。
func runConfigAgent(opts configAgentOptions) error {
	next, err := resolveConfigAgentOptions(opts)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]config.AgentConfig)
	}
	agentCfg := migrateACPAgentConfig(cfg.Agents[next.Name], next)
	cfg.Agents[next.Name] = agentCfg
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.ValidateClaudeACPAgents(); err != nil {
		return err
	}
	if err := probeConfigAgent(next, agentCfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	printConfigAgentResult(next)
	return nil
}

// probeConfigAgent 在写入 Claude 配置前完成 ACP initialize 握手。
func probeConfigAgent(opts configAgentOptions, agentCfg config.AgentConfig) error {
	if opts.Name != "claude" {
		return nil
	}
	if opts.Probe == nil {
		return fmt.Errorf("Claude ACP 能力探针未配置")
	}
	if err := opts.Probe(opts.Name, agentCfg); err != nil {
		return fmt.Errorf("Claude ACP 能力预检失败: %w", err)
	}
	return nil
}

// resolveConfigAgentOptions 解析并校验外部命令路径，避免把无效配置写入磁盘。
func resolveConfigAgentOptions(opts configAgentOptions) (configAgentOptions, error) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = config.LookPath
	}
	opts.Name = strings.TrimSpace(opts.Name)
	if opts.Name == "" {
		opts.Name = defaultConfigAgentName
	}
	command := strings.TrimSpace(opts.Command)
	if command == "" && opts.Name == "claude" {
		command = "claude-agent-acp"
	}
	if command == "" {
		return configAgentOptions{}, fmt.Errorf("agent %q 必须指定 --command", opts.Name)
	}
	resolved, err := lookPath(command)
	if err != nil {
		return configAgentOptions{}, fmt.Errorf("找不到 ACP 命令 %q: %w", command, err)
	}
	opts.Command = resolved
	opts.LocalCommand, err = resolveOptionalLocalCommand(opts, lookPath)
	return opts, err
}

// resolveOptionalLocalCommand 自动发现可选本地命令；用户显式配置错误时必须返回失败。
func resolveOptionalLocalCommand(opts configAgentOptions, lookPath configAgentLookPath) (string, error) {
	localCommand := strings.TrimSpace(opts.LocalCommand)
	if localCommand == "" && opts.Name == "claude" {
		localCommand = "claude"
	}
	if localCommand == "" {
		return "", nil
	}
	resolved, err := lookPath(localCommand)
	if err != nil {
		if strings.TrimSpace(opts.LocalCommand) == "" {
			return "", nil
		}
		return "", fmt.Errorf("找不到本地辅助命令 %q: %w", localCommand, err)
	}
	return resolved, nil
}

// migrateACPAgentConfig 只替换运行方式，保留会话模型、环境和进度等业务配置。
func migrateACPAgentConfig(current config.AgentConfig, opts configAgentOptions) config.AgentConfig {
	current.Type = "acp"
	current.Command = opts.Command
	current.LocalCommand = opts.LocalCommand
	if opts.Name == "claude" {
		current.Args = nil
		if strings.TrimSpace(current.Model) == "" {
			current.Model = "sonnet"
		}
	}
	return current
}

// printConfigAgentResult 输出最终落盘位置和重启要求。
func printConfigAgentResult(opts configAgentOptions) {
	fmt.Printf("已将 %s 配置为 ACP 后端：%s\n", opts.Name, opts.Command)
	if opts.LocalCommand != "" {
		fmt.Printf("本地辅助命令：%s\n", opts.LocalCommand)
	}
	if path, err := config.ConfigPath(); err == nil {
		fmt.Printf("已写入：%s\n", path)
	}
	fmt.Println("如 WeClaw 正在运行，请重启后生效。")
}
