package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/spf13/cobra"
)

var (
	companionAgentFlag string
	companionCwdFlag   string
)

func init() {
	companionCmd.Flags().StringVar(&companionAgentFlag, "agent", "opencode", "要连接的 Agent 名称")
	companionCmd.Flags().StringVar(&companionCwdFlag, "cwd", "", "工作目录")
	rootCmd.AddCommand(companionCmd)
}

var companionCmd = &cobra.Command{
	Use:   "companion",
	Short: "启动本地可见 CLI Companion",
	RunE:  runCompanionCommand,
}

func runCompanionCommand(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cwd, err := resolveCompanionCwd(companionCwdFlag)
	if err != nil {
		return err
	}
	endpoint, err := agent.ReadCompanionEndpoint(companionAgentFlag, cwd)
	if err != nil {
		return fmt.Errorf("读取 Companion 入口失败，请先启动 WeClaw 后台 Agent: %w", err)
	}
	runtime, err := createCompanionRuntime(endpoint)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return agent.RunCompanionClient(ctx, endpoint, runtime)
}

type companionRuntime interface {
	agent.CompanionRequestHandler
	Close() error
}

func createCompanionRuntime(endpoint agent.CompanionEndpoint) (companionRuntime, error) {
	switch strings.ToLower(endpoint.Agent) {
	case "opencode":
		return newOpenCodeCompanionRuntime(endpoint), nil
	case "codex":
		return newCodexAppCompanionRuntime(endpoint), nil
	default:
		return nil, fmt.Errorf("暂不支持 %s Companion，本轮先支持 opencode 和 codex", endpoint.Agent)
	}
}

func resolveCompanionCwd(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "."
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("解析工作目录失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("工作目录不存在: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("工作目录不是目录: %s", abs)
	}
	return abs, nil
}
