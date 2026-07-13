package cmd

import (
	"context"
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

type preparedStart struct {
	cfg *config.Config
	run func() error
}

type startPreparationOps struct {
	loadConfig func() (*config.Config, error)
	preflight  func(context.Context, *config.Config) error
	start      func(*config.Config) error
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
	start := runBackgroundStart
	if foregroundFlag {
		start = runForegroundStart
	}
	prepared, err := prepareConfiguredStart(cmd.Context(), start)
	if err != nil {
		return err
	}
	return prepared.run()
}

// loadStartConfig 加载启动配置，并统一包装配置文件错误。
func loadStartConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	return cfg, nil
}

// prepareConfiguredStart 使用正式依赖生成可延迟执行的已预检启动闭包。
func prepareConfiguredStart(ctx context.Context, start func(*config.Config) error) (preparedStart, error) {
	return prepareStart(ctx, startPreparationOps{
		loadConfig: loadStartConfig,
		preflight:  preflightStartConfig,
		start:      start,
	})
}

// prepareStart 固化一次配置快照，避免停止服务前后重复加载配置。
func prepareStart(ctx context.Context, ops startPreparationOps) (preparedStart, error) {
	cfg, err := ops.loadConfig()
	if err != nil {
		return preparedStart{}, err
	}
	if err := ops.preflight(ctx, cfg); err != nil {
		return preparedStart{}, fmt.Errorf("启动预检失败: %w", err)
	}
	return preparedStart{cfg: cfg, run: func() error { return ops.start(cfg) }}, nil
}

// preflightStartConfig 验证 Claude ACP adapter 可执行且具备会话列表与恢复能力。
func preflightStartConfig(ctx context.Context, cfg *config.Config) error {
	modified := config.DetectAndConfigure(cfg)
	err := cfg.PreflightClaudeACPAgents(config.ClaudeACPPreflightOptions{
		LookPath: config.LookPath,
		Probe: func(name string, agentCfg config.AgentConfig) error {
			return defaultClaudeACPProbe(ctx, name, agentCfg)
		},
	})
	if err != nil || !modified {
		return err
	}
	return persistDetectedStartConfig(modified, cfg, config.Save)
}

// persistDetectedStartConfig 确保后台子进程能重新加载同一份预检配置。
func persistDetectedStartConfig(modified bool, cfg *config.Config, save func(*config.Config) error) error {
	if !modified {
		return nil
	}
	if err := save(cfg); err != nil {
		return fmt.Errorf("保存自动探测配置失败: %w", err)
	}
	return nil
}
