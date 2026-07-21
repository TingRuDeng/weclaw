package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

const githubRepo = "TingRuDeng/weclaw"

var updateRestartFlag bool

func init() {
	updateCmd.Flags().BoolVar(&updateRestartFlag, "restart", false, "更新后重启 WeClaw")
	updateCmd.Flags().BoolVar(&restartForceFlag, "force", false, "即使有运行中任务也强制重启")
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
	if !overridden {
		latest, err = getLatestVersion()
		if err != nil {
			return fmt.Errorf("检查最新版本失败: %w", err)
		}
	}
	return finishUpdate(
		cmd.Context(), Version, latest, updateRestartFlag, restartForceFlag,
		applyUpdate, defaultUpdateCompletionOps(), os.Stdout,
	)
}

// finishUpdate 只在实际替换二进制或显式要求重启时执行启动预检。
func finishUpdate(
	ctx context.Context,
	current string,
	latest string,
	restart bool,
	force bool,
	apply func(string) error,
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
	if err := apply(latest); err != nil {
		return err
	}
	return completeUpdate(ctx, restart, force, completion)
}

// applyUpdate 下载、校验并原子替换当前可执行文件。
func applyUpdate(latest string) error {
	fmt.Printf("当前版本: %s -> 最新版本: %s\n", Version, latest)
	filename, err := releaseAssetNameForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	fmt.Printf("正在下载 %s/%s...\n", latest, filename)
	tmpFile, err := downloadReleaseAsset(latest, filename)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer os.Remove(tmpFile)
	if err := verifyReleaseAssetChecksum(latest, filename, tmpFile); err != nil {
		return fmt.Errorf("校验发布文件摘要失败: %w", err)
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位当前可执行文件失败: %w", err)
	}
	if resolved, err := resolveSymlink(exePath); err == nil {
		exePath = resolved
	}
	if err := validateUpdateTargetMatchesRuntime(exePath); err != nil {
		return err
	}

	if err := replaceBinary(tmpFile, exePath); err != nil {
		return fmt.Errorf("替换可执行文件失败: %w", err)
	}
	if runtime.GOOS == "darwin" {
		exec.Command("xattr", "-d", "com.apple.quarantine", exePath).Run()
		exec.Command("xattr", "-d", "com.apple.provenance", exePath).Run()
	}
	fmt.Printf("已更新到 %s\n", latest)
	return nil
}

type updateCompletionOps struct {
	prepare    func(context.Context) (preparedStart, error)
	ensureSafe func(context.Context, bool, *config.Config) error
	running    func() bool
	stop       func() error
	out        io.Writer
}

// defaultUpdateCompletionOps 复用正式启动预检，并保留更新命令原有的运行状态语义。
func defaultUpdateCompletionOps() updateCompletionOps {
	return updateCompletionOps{
		prepare: func(ctx context.Context) (preparedStart, error) {
			return prepareConfiguredStart(ctx, runBackgroundStart)
		},
		ensureSafe: ensureRestartSafeWithConfig,
		running:    weclawIsRunningForRestart,
		stop:       stopAllWeclaw,
		out:        os.Stdout,
	}
}

// completeUpdate 根据重启选项把预检错误转换为警告或停止前硬失败。
func completeUpdate(ctx context.Context, restart bool, force bool, ops updateCompletionOps) error {
	prepared, err := ops.prepare(ctx)
	if err != nil {
		if restart {
			return err
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
		return err
	}
	return restartUpdatedService(prepared, ops)
}

// restartUpdatedService 仅在旧服务实际运行时执行停止与已预检启动闭包。
func restartUpdatedService(prepared preparedStart, ops updateCompletionOps) error {
	if !ops.running() {
		fmt.Fprintln(ops.out, "更新完成；当前服务未运行，请执行 weclaw start。")
		return nil
	}
	fmt.Fprintln(ops.out, "正在停止旧服务...")
	if err := ops.stop(); err != nil {
		log.Printf("停止旧服务失败：%v", err)
		return fmt.Errorf("更新完成，但停止旧服务失败: %w", err)
	}
	fmt.Fprintln(ops.out, "正在启动新版本...")
	if err := prepared.run(); err != nil {
		log.Printf("启动新版本失败：%v", err)
		return fmt.Errorf("更新完成，但启动新服务失败: %w", err)
	}
	return nil
}
