package cmd

import (
	"fmt"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

var (
	foregroundFlag bool
	apiAddrFlag    string
)

// init 注册 start 命令及其前台运行、API 地址参数。
func init() {
	startCmd.Flags().BoolVarP(&foregroundFlag, "foreground", "f", false, "前台运行，默认后台运行")
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "HTTP API 监听地址，默认 127.0.0.1:18011")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "启动消息服务",
	RunE:  runStart,
}

// runStart 加载配置后按前台或后台模式进入对应启动流程。
func runStart(cmd *cobra.Command, args []string) error {
	daemonLog, err := configureDaemonLogging()
	if err != nil {
		return err
	}
	if daemonLog != nil {
		defer daemonLog.Close()
	}
	cfg, err := loadStartConfig()
	if err != nil {
		return err
	}
	if !foregroundFlag {
		return runBackgroundStart(cfg)
	}
	return runForegroundStart(cfg)
}

// loadStartConfig 加载并校验启动配置，避免后台进程接收无效 Claude 配置。
func loadStartConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if err := cfg.ValidateClaudeACPAgents(); err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}
