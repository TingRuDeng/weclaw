package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

var stopForceFlag bool

func init() {
	stopCmd.Flags().BoolVar(
		&stopForceFlag,
		"force",
		false,
		"中断本地任务，关闭 Codex App，并强制停止当前用户的 Codex Host",
	)
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "协调停止 WeClaw 服务与受管 Codex Host",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStopWithOptions(cmd.Context(), stopForceFlag, defaultStopOps())
	},
}

type stopOps struct {
	loadConfig         func() (*config.Config, error)
	isRunning          func() bool
	prepare            func(context.Context, bool, *config.Config) error
	prepareWithOptions func(context.Context, bool, bool, *config.Config) error
	stop               func() error
	legacyStop         func(context.Context, *config.Config, func() error) error
	forceLegacy        func(context.Context, *config.Config, error, func(bool) error) error
	cancel             func(context.Context, *config.Config) error
	acquireLease       func() (io.Closer, error)
	offlineForce       func(*config.Config) error
	out                io.Writer
}

func defaultStopOps() stopOps {
	return stopOps{
		loadConfig: config.Load,
		isRunning:  weclawIsRunningForRestart,
		prepare:    beginRestartDrainWithConfig,
		prepareWithOptions: func(ctx context.Context, forceDrain bool, forceTerminate bool, cfg *config.Config) error {
			return beginRestartDrainWithControl(ctx, forceDrain, forceTerminate, cfg)
		},
		stop:        stopAllWeclaw,
		legacyStop:  stopLegacyRuntime,
		forceLegacy: forceLegacyRuntime,
		cancel:      cancelRestartDrain,
		acquireLease: func() (io.Closer, error) {
			return agent.AcquireCodexRestartLease()
		},
		offlineForce: func(cfg *config.Config) error {
			return ensureOfflineCodexRestartSafe(cfg, true)
		},
		out: os.Stdout,
	}
}

func runStop(ctx context.Context, ops stopOps) error {
	return runStopWithOptions(ctx, false, ops)
}

func runStopWithOptions(ctx context.Context, force bool, ops stopOps) error {
	running := ops.isRunning != nil && ops.isRunning()
	if running {
		cfg, err := ops.loadConfig()
		if err != nil {
			return fmt.Errorf("读取停止配置: %w", err)
		}
		prepare := ops.prepare
		if ops.prepareWithOptions != nil {
			prepare = func(ctx context.Context, forceDrain bool, cfg *config.Config) error {
				return ops.prepareWithOptions(ctx, forceDrain, force, cfg)
			}
		}
		if prepare == nil {
			return fmt.Errorf("协调停止缺少安全预检")
		}
		if err := prepare(ctx, true, cfg); err != nil {
			if errors.Is(err, errCoordinatedRestartUnsupported) {
				if force {
					if ops.forceLegacy != nil {
						if err := ops.forceLegacy(ctx, cfg, err, nil); err != nil {
							return err
						}
						return writeStopConfirmation(ops.out)
					}
					return err
				}
				if ops.legacyStop == nil {
					return err
				}
				if migrationErr := ops.legacyStop(ctx, cfg, ops.stop); migrationErr != nil {
					return migrationErr
				}
				return writeStopConfirmation(ops.out)
			}
			return compensateRestartDrain(err, ops.cancel, cfg)
		}
		if err := ops.stop(); err != nil {
			return compensateRestartDrain(err, ops.cancel, cfg)
		}
		return writeStopConfirmation(ops.out)
	}
	if force {
		cfg, err := ops.loadConfig()
		if err != nil {
			return fmt.Errorf("读取强制停止配置: %w", err)
		}
		if ops.acquireLease != nil {
			lease, leaseErr := ops.acquireLease()
			if leaseErr != nil {
				return fmt.Errorf("无法开始强制停止: %w；请先退出所有 weclaw codex cli", leaseErr)
			}
			if lease != nil {
				defer lease.Close()
			}
		}
		if ops.offlineForce == nil {
			return fmt.Errorf("强制停止缺少离线 Codex 终止入口")
		}
		if err := ops.offlineForce(cfg); err != nil {
			return err
		}
		return writeStopConfirmation(ops.out)
	}
	if err := ops.stop(); err != nil {
		return err
	}
	return writeStopConfirmation(ops.out)
}

func writeStopConfirmation(out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	_, err := fmt.Fprintln(out, "WeClaw 已停止")
	return err
}
