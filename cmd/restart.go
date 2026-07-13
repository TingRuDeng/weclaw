package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

var restartForceFlag bool

func init() {
	restartCmd.Flags().BoolVar(&restartForceFlag, "force", false, "即使有运行中任务也强制重启")
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "重启后台 WeClaw 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRestart(context.Background(), restartForceFlag, defaultRestartOps())
	},
}

type restartOps struct {
	ensureSafe func(context.Context, bool) error
	isRunning  func() bool
	stop       func() error
	start      func() error
	out        io.Writer
}

type configuredDaemonStartOps struct {
	loadConfig func() (*config.Config, error)
	start      func(*config.Config) error
}

func defaultRestartOps() restartOps {
	return restartOps{
		ensureSafe: ensureConfiguredRestartSafe,
		isRunning:  weclawIsRunningForRestart,
		stop:       stopAllWeclaw,
		start:      startConfiguredDaemon,
		out:        os.Stdout,
	}
}

// startConfiguredDaemon 复用 start 命令的配置校验和登录前置流程。
func startConfiguredDaemon() error {
	return startConfiguredDaemonWithOps(configuredDaemonStartOps{
		loadConfig: loadStartConfig,
		start:      runBackgroundStart,
	})
}

// startConfiguredDaemonWithOps 保证配置预检失败时不会派生后台子进程。
func startConfiguredDaemonWithOps(ops configuredDaemonStartOps) error {
	cfg, err := ops.loadConfig()
	if err != nil {
		return err
	}
	return ops.start(cfg)
}

func runRestart(ctx context.Context, force bool, ops restartOps) error {
	if err := ops.ensureSafe(ctx, force); err != nil {
		return err
	}
	if ops.isRunning() {
		fmt.Fprintln(ops.out, "正在停止 WeClaw...")
		if err := ops.stop(); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(ops.out, "未检测到运行中的 WeClaw，直接启动...")
	}
	fmt.Fprintln(ops.out, "正在启动 WeClaw...")
	return ops.start()
}

// weclawIsRunningForRestart 只在 restart 入口判断是否需要执行停止阶段。
func weclawIsRunningForRestart() bool {
	pid, err := readPid()
	if err != nil {
		return false
	}
	if !processExists(pid) {
		_ = removePIDFile()
		return false
	}
	if !runtimeLockBusy() {
		_ = removePIDFile()
		return false
	}
	return true
}
