package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const maxUpdateDownloadBytes = 128 * 1024 * 1024

var updateHTTPClient = &http.Client{Timeout: updateHTTPTimeout}

func downloadFile(url string) (string, error) {
	req, err := newGitHubRequest(http.MethodGet, url)
	if err != nil {
		return "", err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxUpdateDownloadBytes {
		return "", fmt.Errorf("download is too large: %d > %d", resp.ContentLength, maxUpdateDownloadBytes)
	}

	tmp, err := os.CreateTemp("", "weclaw-update-*")
	if err != nil {
		return "", err
	}

	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxUpdateDownloadBytes+1))
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if written > maxUpdateDownloadBytes {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("download exceeds %d bytes", maxUpdateDownloadBytes)
	}
	tmp.Close()

	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}

	return tmp.Name(), nil
}

// validateUpdateTargetMatchesRuntime 避免更新路径和正在运行的服务路径错位。
func validateUpdateTargetMatchesRuntime(exePath string) error {
	state, err := readRuntimeState()
	if err != nil || !processExists(state.PID) || strings.TrimSpace(state.Exe) == "" {
		return nil
	}
	runningPath := state.Exe
	if resolved, err := resolveSymlink(runningPath); err == nil {
		runningPath = resolved
	}
	if runningPath == exePath {
		return nil
	}
	return fmt.Errorf("running weclaw uses %s, but update target is %s; please run update from the same installation path", runningPath, exePath)
}

func replaceBinary(src, dst string) error {
	// 先尝试直接替换；失败时 Unix 平台再交给 sudo cp。
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	if runtime.GOOS != "windows" {
		fmt.Printf("Installing to %s (requires sudo)...\n", dst)
		cmd := exec.Command("sudo", "cp", src, dst)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("cannot write to %s", dst)
}

func resolveSymlink(path string) (string, error) {
	for {
		target, err := os.Readlink(path)
		if err != nil {
			return path, nil
		}
		if !strings.HasPrefix(target, "/") {
			dir := path[:strings.LastIndex(path, "/")+1]
			target = dir + target
		}
		path = target
	}
}
