package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/fastclaw-ai/weclaw/agent"
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
	Short: "协调重启 WeClaw 服务与受管 Codex Host",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRestart(context.Background(), restartForceFlag, defaultRestartOps())
	},
}

type restartOps struct {
	prepare        func(context.Context) (preparedStart, error)
	acquireLease   func() (io.Closer, error)
	ensureSafe     func(context.Context, bool, *config.Config) error
	offlineSafe    func(*config.Config) error
	isRunning      func() bool
	stop           func() error
	isSystemd      func() bool
	restartSystemd func() error
	cancelDrain    func(context.Context, *config.Config) error
	out            io.Writer
}

func defaultRestartOps() restartOps {
	return restartOps{
		prepare: func(ctx context.Context) (preparedStart, error) {
			return prepareConfiguredStart(ctx, runBackgroundStart)
		},
		acquireLease:   func() (io.Closer, error) { return agent.AcquireCodexRestartLease() },
		ensureSafe:     beginRestartDrainWithConfig,
		offlineSafe:    ensureOfflineCodexRestartSafe,
		isRunning:      weclawIsRunningForRestart,
		stop:           stopAllWeclaw,
		isSystemd:      isSystemdManagedRuntime,
		restartSystemd: restartSystemdService,
		cancelDrain:    cancelRestartDrain,
		out:            os.Stdout,
	}
}

// runRestart 在停止旧服务前固化已预检的配置和启动闭包。
func runRestart(ctx context.Context, force bool, ops restartOps) error {
	prepared, err := ops.prepare(ctx)
	if err != nil {
		return err
	}
	if err := ops.ensureSafe(ctx, force, prepared.cfg); err != nil {
		if errors.Is(err, errCoordinatedRestartUnsupported) {
			return err
		}
		return compensateRestartDrain(err, ops.cancelDrain, prepared.cfg)
	}
	running := ops.isRunning()
	if !running {
		if ops.acquireLease != nil {
			lease, leaseErr := ops.acquireLease()
			if leaseErr != nil {
				return fmt.Errorf("无法开始协调重启: %w；请先退出所有 weclaw codex cli", leaseErr)
			}
			defer lease.Close()
		}
		if ops.offlineSafe != nil {
			if err := ops.offlineSafe(prepared.cfg); err != nil {
				return err
			}
		}
	}
	if running {
		if ops.isSystemd != nil && ops.isSystemd() {
			fmt.Fprintln(ops.out, "正在通过 systemd 重启 WeClaw...")
			if ops.restartSystemd == nil {
				return compensateRestartDrain(
					fmt.Errorf("systemd restart is unavailable"), ops.cancelDrain, prepared.cfg,
				)
			}
			if err := ops.restartSystemd(); err != nil {
				return compensateRestartDrain(err, ops.cancelDrain, prepared.cfg)
			}
			return nil
		}
		fmt.Fprintln(ops.out, "正在停止 WeClaw...")
		if err := ops.stop(); err != nil {
			return compensateRestartDrain(err, ops.cancelDrain, prepared.cfg)
		}
	} else {
		fmt.Fprintln(ops.out, "未检测到运行中的 WeClaw，直接启动...")
	}
	fmt.Fprintln(ops.out, "正在启动 WeClaw...")
	return prepared.run()
}

func compensateRestartDrain(
	cause error,
	cancelFn func(context.Context, *config.Config) error,
	cfg *config.Config,
) error {
	if cancelFn == nil {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), restartDrainTimeout)
	defer cancel()
	if err := cancelFn(ctx, cfg); err != nil {
		return errors.Join(cause, fmt.Errorf("恢复重启事务失败: %w", err))
	}
	return cause
}

func ensureOfflineCodexRestartSafe(cfg *config.Config) error {
	configured := false
	for _, candidate := range cfg.Agents {
		if isCodexAppServerAgent(candidate) {
			configured = true
			break
		}
	}
	if !configured {
		return nil
	}
	socketExists, processExists := agent.CodexDesktopFrontendPresence()
	if socketExists || processExists {
		return fmt.Errorf("%w；请完整退出 Codex App 后重试", agent.ErrCodexDesktopFrontendActive)
	}
	return nil
}

func isSystemdManagedRuntime() bool {
	state, err := readRuntimeState()
	return err == nil && state.Mode == "systemd"
}

func restartSystemdService() error {
	name := "systemctl"
	args := []string{"restart", "weclaw.service"}
	if os.Geteuid() != 0 {
		name = "sudo"
		args = append([]string{"systemctl"}, args...)
	}
	command := exec.Command(name, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("通过 systemd 重启 WeClaw 失败: %w", err)
	}
	return nil
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
