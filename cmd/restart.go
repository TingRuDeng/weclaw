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
	prepare    func(context.Context) (preparedStart, error)
	ensureSafe func(context.Context, bool, *config.Config) error
	isRunning  func() bool
	stop       func() error
	out        io.Writer
}

func defaultRestartOps() restartOps {
	return restartOps{
		prepare: func(ctx context.Context) (preparedStart, error) {
			return prepareConfiguredStart(ctx, runBackgroundStart)
		},
		ensureSafe: ensureRestartSafeWithConfig,
		isRunning:  weclawIsRunningForRestart,
		stop:       stopAllWeclaw,
		out:        os.Stdout,
	}
}

// runRestart 在停止旧服务前固化已预检的配置和启动闭包。
func runRestart(ctx context.Context, force bool, ops restartOps) error {
	prepared, err := ops.prepare(ctx)
	if err != nil {
		return err
	}
	if err := ops.ensureSafe(ctx, force, prepared.cfg); err != nil {
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
	return prepared.run()
}

// ensureRestartSafeWithConfig 使用同一配置快照检查运行中任务，避免预检后再次读取磁盘。
func ensureRestartSafeWithConfig(ctx context.Context, force bool, cfg *config.Config) error {
	state, err := readRuntimeState()
	if err != nil || !processExists(state.PID) {
		return nil
	}
	return ensureRestartSafe(ctx, restartSafetyOptions{
		apiAddr: cfg.APIAddr, apiToken: cfg.APIToken,
		processExists: true, force: force,
	})
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
