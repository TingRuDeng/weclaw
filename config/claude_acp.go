package config

import (
	"fmt"
)

const claudeMigrationCommand = "weclaw config agent --name claude"

// ValidateClaudeACPAgents 拒绝 Claude 旧 CLI 后端，并给出可直接执行的迁移入口。
func (c *Config) ValidateClaudeACPAgents() error {
	if c == nil {
		return nil
	}
	agentCfg, ok := c.Agents["claude"]
	if !ok || agentCfg.Type == "acp" {
		return nil
	}
	return fmt.Errorf("agent \"claude\" 仅支持 ACP 后端；请执行 %s", claudeMigrationCommand)
}

// detectedLocalCommand 返回 ACP 会话可选的本地交接命令，缺失不影响远程能力。
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
