package config

import (
	"fmt"
	"strings"
)

const claudeMigrationCommand = "weclaw config agent --name claude"

// ClaudeACPPreflightOptions 注入命令解析与能力探针，便于所有启动入口复用同一校验。
type ClaudeACPPreflightOptions struct {
	LookPath func(string) (string, error)
	Probe    func(string, AgentConfig) error
}

// ValidateClaudeACPAgents 拒绝 Claude 旧 CLI 后端，并给出可直接执行的迁移入口。
func (c *Config) ValidateClaudeACPAgents() error {
	if c == nil {
		return nil
	}
	agentCfg, ok := c.Agents["claude"]
	if !ok {
		return nil
	}
	if agentCfg.Type != "acp" {
		return fmt.Errorf("agent \"claude\" 仅支持 ACP 后端；请执行 %s", claudeMigrationCommand)
	}
	if strings.TrimSpace(agentCfg.Command) == "" {
		return fmt.Errorf("agent \"claude\" 缺少 ACP 命令；请执行 %s", claudeMigrationCommand)
	}
	return nil
}

// PreflightClaudeACPAgents 校验 Claude ACP 的类型、命令与会话能力，并固化解析后的命令路径。
func (c *Config) PreflightClaudeACPAgents(opts ClaudeACPPreflightOptions) error {
	if err := c.ValidateClaudeACPAgents(); err != nil || c == nil {
		return err
	}
	agentCfg, ok := c.Agents["claude"]
	if !ok {
		return nil
	}
	if opts.LookPath == nil || opts.Probe == nil {
		return fmt.Errorf("agent \"claude\" ACP 预检依赖未配置")
	}
	resolved, err := opts.LookPath(agentCfg.Command)
	if err != nil {
		return fmt.Errorf("找不到 agent \"claude\" ACP 命令 %q；请执行 %s: %w", agentCfg.Command, claudeMigrationCommand, err)
	}
	agentCfg.Command = resolved
	if err := opts.Probe("claude", agentCfg); err != nil {
		return fmt.Errorf("agent \"claude\" ACP 能力预检失败: %w", err)
	}
	c.Agents["claude"] = agentCfg
	return nil
}

// detectedLocalCommand 返回 ACP 会话可选的本地交接/账号能力回退命令，缺失不影响远程能力。
func detectedLocalCommand(agentName string) string {
	if agentName != "claude" {
		return ""
	}
	path, err := detectLookPath("claude")
	if err != nil {
		return ""
	}
	return path
}
