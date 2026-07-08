package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

const githubRepo = "TingRuDeng/weclaw"

var updateRestartFlag bool

func init() {
	updateCmd.Flags().BoolVar(&updateRestartFlag, "restart", false, "Restart weclaw after updating")
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
	// 1. Get latest version
	fmt.Println("正在检查更新...")
	latest, err := getLatestVersion()
	if err != nil {
		return fmt.Errorf("检查最新版本失败: %w", err)
	}

	if latest == Version {
		fmt.Printf("已是最新版本 (%s)\n", Version)
		return nil
	}

	fmt.Printf("当前版本: %s -> 最新版本: %s\n", Version, latest)

	// 2. Download new binary
	filename, err := releaseAssetNameForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, latest, filename)

	fmt.Printf("正在下载 %s...\n", url)
	tmpFile, err := downloadFile(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer os.Remove(tmpFile)
	if err := verifyReleaseAssetChecksum(latest, filename, tmpFile); err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}

	// 3. Replace current binary
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	// Resolve symlinks
	if resolved, err := resolveSymlink(exePath); err == nil {
		exePath = resolved
	}
	if err := validateUpdateTargetMatchesRuntime(exePath); err != nil {
		return err
	}

	if err := replaceBinary(tmpFile, exePath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	// Clear macOS quarantine/provenance attributes to avoid Gatekeeper killing the binary
	if runtime.GOOS == "darwin" {
		exec.Command("xattr", "-d", "com.apple.quarantine", exePath).Run()
		exec.Command("xattr", "-d", "com.apple.provenance", exePath).Run()
	}

	fmt.Printf("Updated to %s\n", latest)

	if !updateRestartFlag {
		fmt.Println("Update complete. Run 'weclaw restart' when you are ready.")
		return nil
	}
	if err := ensureConfiguredRestartSafe(context.Background(), restartForceFlag); err != nil {
		return err
	}

	// 4. Restart only when explicitly requested
	pid, pidErr := readPid()
	if pidErr == nil && processExists(pid) {
		fmt.Println("Stopping old process...")
		if err := stopAllWeclaw(); err != nil {
			log.Printf("Failed to stop old process: %v", err)
			fmt.Println("Update complete. Please run 'weclaw stop' and 'weclaw start' manually.")
			return nil
		}

		fmt.Println("Starting new version...")
		if err := runDaemon(); err != nil {
			log.Printf("Failed to restart: %v", err)
			fmt.Println("Update complete. Please run 'weclaw start' manually.")
		}
	} else {
		fmt.Println("Update complete. Run 'weclaw start' to start.")
	}

	return nil
}
