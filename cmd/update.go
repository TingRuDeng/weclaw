package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

const githubRepo = "TingRuDeng/weclaw"
const giteeRepo = "jimdeng891/weclaw"

var updateRestartFlag bool
var updateSourceFlag string

func init() {
	updateCmd.Flags().BoolVar(&updateRestartFlag, "restart", false, "更新后协调重启 WeClaw 与受管 Codex Host")
	updateCmd.Flags().BoolVar(&restartForceFlag, "force", false, "即使有运行中任务也强制重启")
	updateCmd.Flags().StringVar(&updateSourceFlag, "source", "", "更新来源：auto、github 或 gitee（默认读取配置）")
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "查看当前版本",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("weclaw %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "更新 WeClaw",
	RunE:  runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	fmt.Println("正在检查更新...")
	latest, overridden, err := updateReleaseTagOverride()
	if err != nil {
		return fmt.Errorf("检查目标版本失败: %w", err)
	}
	sources := []releaseSource{releaseSourceGitHub}
	if !overridden {
		cfg, loadErr := config.Load()
		if loadErr != nil {
			return fmt.Errorf("读取更新来源配置失败: %w", loadErr)
		}
		source, sourceErr := effectiveReleaseSource(updateSourceFlag, cfg.UpdateSource)
		if sourceErr != nil {
			return sourceErr
		}
		latest, sources, err = resolveLatestRelease(source, getLatestVersion, func() (string, error) {
			return getGiteeLatestVersionFromBase(giteeAPIBaseURL)
		})
		if err != nil {
			return fmt.Errorf("检查最新版本失败: %w", err)
		}
	}
	return finishUpdate(
		cmd.Context(), Version, latest, updateRestartFlag, restartForceFlag,
		func(version string) (updateTransaction, error) {
			return applyUpdateFromSources(version, sources)
		},
		defaultUpdateCompletionOps(), os.Stdout,
	)
}

func effectiveReleaseSource(flagValue string, configured string) (releaseSource, error) {
	if strings.TrimSpace(flagValue) != "" {
		return parseReleaseSource(flagValue)
	}
	return parseReleaseSource(configured)
}

type updateTransaction struct {
	commit   func()
	rollback func() error
}

func (transaction updateTransaction) Commit() {
	if transaction.commit != nil {
		transaction.commit()
	}
}

func (transaction updateTransaction) Rollback() error {
	if transaction.rollback == nil {
		return nil
	}
	return transaction.rollback()
}

// finishUpdate 只在实际替换二进制或显式要求重启时执行启动预检。
func finishUpdate(
	ctx context.Context,
	current string,
	latest string,
	restart bool,
	force bool,
	apply func(string) (updateTransaction, error),
	completion updateCompletionOps,
	out io.Writer,
) error {
	if latest == current {
		fmt.Fprintf(out, "已是最新版本 (%s)\n", current)
		if !restart {
			return nil
		}
		return completeUpdate(ctx, true, force, completion)
	}
	if stableUpdateReleaseTagPattern.MatchString(current) && stableUpdateReleaseTagPattern.MatchString(latest) {
		comparison, err := compareStableReleaseTags(current, latest)
		if err != nil {
			return fmt.Errorf("比较更新版本失败: %w", err)
		}
		if comparison > 0 {
			return fmt.Errorf("拒绝从 %s 降级到 %s", current, latest)
		}
	}
	transaction, err := apply(latest)
	if err != nil {
		return err
	}
	if err := completeUpdateWithRollback(ctx, restart, force, completion, transaction.Rollback); err != nil {
		return err
	}
	transaction.Commit()
	return nil
}

func compareStableReleaseTags(left string, right string) (int, error) {
	parse := func(tag string) ([3]int, error) {
		var version [3]int
		if !stableUpdateReleaseTagPattern.MatchString(tag) {
			return version, fmt.Errorf("版本 %q 必须是 vX.Y.Z 格式", tag)
		}
		if _, err := fmt.Sscanf(tag, "v%d.%d.%d", &version[0], &version[1], &version[2]); err != nil {
			return version, fmt.Errorf("解析版本 %q: %w", tag, err)
		}
		return version, nil
	}
	leftVersion, err := parse(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := parse(right)
	if err != nil {
		return 0, err
	}
	for index := range leftVersion {
		if leftVersion[index] < rightVersion[index] {
			return -1, nil
		}
		if leftVersion[index] > rightVersion[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func applyUpdateFromSources(latest string, sources []releaseSource) (updateTransaction, error) {
	fmt.Printf("当前版本: %s -> 最新版本: %s\n", Version, latest)
	filename, err := releaseAssetNameForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return updateTransaction{}, err
	}

	fmt.Printf("正在下载 %s/%s...\n", latest, filename)
	tmpFile, err := tryReleaseSources(sources, func(source releaseSource) (string, error) {
		assetPath, err := downloadReleaseAssetFromSource(source, latest, filename)
		if err != nil {
			return "", err
		}
		if err := verifyReleaseAssetChecksumFromSource(source, latest, filename, assetPath); err != nil {
			_ = os.Remove(assetPath)
			return "", fmt.Errorf("校验发布文件摘要失败: %w", err)
		}
		return assetPath, nil
	})
	if err != nil {
		return updateTransaction{}, fmt.Errorf("下载失败: %w", err)
	}
	defer os.Remove(tmpFile)
	exePath, err := os.Executable()
	if err != nil {
		return updateTransaction{}, fmt.Errorf("定位当前可执行文件失败: %w", err)
	}
	if resolved, err := resolveSymlink(exePath); err == nil {
		exePath = resolved
	}
	if err := validateUpdateTargetMatchesRuntime(exePath); err != nil {
		return updateTransaction{}, err
	}

	transaction, err := installBinaryWithRollback(tmpFile, exePath)
	if err != nil {
		return updateTransaction{}, fmt.Errorf("替换可执行文件失败: %w", err)
	}
	fmt.Printf("已更新到 %s\n", latest)
	return transaction, nil
}

type updateCompletionOps struct {
	prepare        func(context.Context) (preparedStart, error)
	ensureSafe     func(context.Context, bool, *config.Config) error
	running        func() bool
	stop           func() error
	isSystemd      func() bool
	restartSystemd func() error
	cancelDrain    func(context.Context, *config.Config) error
	out            io.Writer
}

// defaultUpdateCompletionOps 复用正式启动预检，并保留更新命令原有的运行状态语义。
func defaultUpdateCompletionOps() updateCompletionOps {
	return updateCompletionOps{
		prepare: func(ctx context.Context) (preparedStart, error) {
			return prepareConfiguredStart(ctx, runBackgroundStart)
		},
		ensureSafe:     beginRestartDrainWithConfig,
		running:        weclawIsRunningForRestart,
		stop:           stopAllWeclaw,
		isSystemd:      isSystemdManagedRuntime,
		restartSystemd: restartSystemdService,
		cancelDrain:    cancelRestartDrain,
		out:            os.Stdout,
	}
}

// completeUpdate 根据重启选项把预检错误转换为警告或停止前硬失败。
func completeUpdate(ctx context.Context, restart bool, force bool, ops updateCompletionOps) error {
	return completeUpdateWithRollback(ctx, restart, force, ops, nil)
}

func completeUpdateWithRollback(
	ctx context.Context,
	restart bool,
	force bool,
	ops updateCompletionOps,
	rollback func() error,
) error {
	prepared, err := ops.prepare(ctx)
	if err != nil {
		if restart || rollback != nil {
			return rollbackUpdatedBinary(err, rollback, ops.out)
		}
		fmt.Fprintf(ops.out, "警告：Claude ACP 依赖预检失败：%v\n", err)
		fmt.Fprintln(ops.out, "更新完成；修复依赖后运行 weclaw restart。")
		return nil
	}
	if !restart {
		fmt.Fprintln(ops.out, "更新完成；准备就绪后运行 weclaw restart。")
		return nil
	}
	if err := ops.ensureSafe(ctx, force, prepared.cfg); err != nil {
		return rollbackUpdatedBinary(compensateRestartDrain(err, ops.cancelDrain, prepared.cfg), rollback, ops.out)
	}
	return restartUpdatedServiceWithRollback(prepared, ops, rollback)
}

// restartUpdatedService 仅在旧服务实际运行时执行停止与已预检启动闭包。
func restartUpdatedService(prepared preparedStart, ops updateCompletionOps) error {
	return restartUpdatedServiceWithRollback(prepared, ops, nil)
}

func restartUpdatedServiceWithRollback(prepared preparedStart, ops updateCompletionOps, rollback func() error) error {
	if !ops.running() {
		fmt.Fprintln(ops.out, "更新完成；当前服务未运行，请执行 weclaw start。")
		return nil
	}
	if ops.isSystemd != nil && ops.isSystemd() {
		fmt.Fprintln(ops.out, "正在通过 systemd 重启新版本...")
		if ops.restartSystemd == nil {
			return rollbackUpdatedBinary(
				compensateRestartDrain(fmt.Errorf("systemd restart is unavailable"), ops.cancelDrain, prepared.cfg),
				rollback, ops.out,
			)
		}
		if err := ops.restartSystemd(); err != nil {
			return rollbackUpdatedBinary(
				compensateRestartDrain(fmt.Errorf("更新完成，但 systemd 重启失败: %w", err), ops.cancelDrain, prepared.cfg),
				rollback, ops.out,
			)
		}
		return nil
	}
	fmt.Fprintln(ops.out, "正在停止旧服务...")
	if err := ops.stop(); err != nil {
		log.Printf("停止旧服务失败：%v", err)
		cause := compensateRestartDrain(
			fmt.Errorf("更新完成，但停止旧服务失败: %w", err), ops.cancelDrain, prepared.cfg,
		)
		return recoverPreviousUpdate(prepared, ops, cause, rollback)
	}
	fmt.Fprintln(ops.out, "正在启动新版本...")
	if err := prepared.run(); err != nil {
		log.Printf("启动新版本失败：%v", err)
		return recoverPreviousUpdate(prepared, ops, fmt.Errorf("更新完成，但启动新服务失败: %w", err), rollback)
	}
	return nil
}

func rollbackUpdatedBinary(cause error, rollback func() error, out io.Writer) error {
	if rollback == nil {
		return cause
	}
	if err := rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("恢复旧版本失败: %w", err))
	}
	fmt.Fprintln(out, "更新未完成；已恢复旧版本。")
	return cause
}

func recoverPreviousUpdate(prepared preparedStart, ops updateCompletionOps, cause error, rollback func() error) error {
	if rollback == nil {
		return cause
	}
	if err := rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("恢复旧版本失败: %w", err))
	}
	if ops.running() {
		fmt.Fprintln(ops.out, "已恢复旧版本可执行文件；原服务仍在运行。")
		return cause
	}
	fmt.Fprintln(ops.out, "已恢复旧版本，正在重新启动原服务...")
	if err := prepared.run(); err != nil {
		return errors.Join(cause, fmt.Errorf("旧版本已恢复，但原服务重新启动失败: %w", err))
	}
	fmt.Fprintln(ops.out, "旧版本服务已恢复运行。")
	return cause
}
