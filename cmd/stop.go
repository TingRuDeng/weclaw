package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "协调停止 WeClaw 服务与受管 Codex Host",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStop(cmd.Context(), defaultStopOps())
	},
}

type stopOps struct {
	loadConfig func() (*config.Config, error)
	isRunning  func() bool
	prepare    func(context.Context, bool, *config.Config) error
	stop       func() error
	cancel     func(context.Context, *config.Config) error
	out        io.Writer
}

func defaultStopOps() stopOps {
	return stopOps{
		loadConfig: config.Load,
		isRunning:  weclawIsRunningForRestart,
		prepare:    beginRestartDrainWithConfig,
		stop:       stopAllWeclaw,
		cancel:     cancelRestartDrain,
		out:        os.Stdout,
	}
}

func runStop(ctx context.Context, ops stopOps) error {
	if ops.isRunning != nil && ops.isRunning() {
		cfg, err := ops.loadConfig()
		if err != nil {
			return fmt.Errorf("读取停止配置: %w", err)
		}
		if err := ops.prepare(ctx, true, cfg); err != nil {
			if errors.Is(err, errCoordinatedRestartUnsupported) {
				return err
			}
			return compensateRestartDrain(err, ops.cancel, cfg)
		}
		if err := ops.stop(); err != nil {
			return compensateRestartDrain(err, ops.cancel, cfg)
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
