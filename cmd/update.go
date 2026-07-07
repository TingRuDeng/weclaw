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
	Short: "Print the current version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("weclaw %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update weclaw to the latest version",
	RunE:  runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	// 1. Get latest version
	fmt.Println("Checking for updates...")
	latest, err := getLatestVersion()
	if err != nil {
		return fmt.Errorf("failed to check latest version: %w", err)
	}

	if latest == Version {
		fmt.Printf("Already up to date (%s)\n", Version)
		return nil
	}

	fmt.Printf("Current: %s -> Latest: %s\n", Version, latest)

	// 2. Download new binary
	filename, err := releaseAssetNameForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, latest, filename)

	fmt.Printf("Downloading %s...\n", url)
	tmpFile, err := downloadFile(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
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
