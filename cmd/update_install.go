package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const maxUpdateDownloadBytes = 128 * 1024 * 1024
const maxSymlinkDepth = 40

var updateHTTPClient = &http.Client{Timeout: updateHTTPTimeout}

func downloadFile(url string) (string, error) {
	return downloadFileWithAccept(url, "")
}

func downloadFileWithAccept(url string, accept string) (string, error) {
	req, err := newGitHubRequest(http.MethodGet, url)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(accept) != "" {
		req.Header.Set("Accept", accept)
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
	if err := stageAndReplaceBinary(src, dst); err == nil {
		return nil
	}

	if runtime.GOOS != "windows" {
		fmt.Printf("Installing to %s (requires sudo)...\n", dst)
		return sudoStageAndReplaceBinary(src, dst)
	}

	return fmt.Errorf("cannot write to %s", dst)
}

func stageAndReplaceBinary(src string, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	stage, err := os.CreateTemp(filepath.Dir(dst), ".weclaw-update-*")
	if err != nil {
		return err
	}
	stagePath := stage.Name()
	defer os.Remove(stagePath)
	if err := copyUpdateBinary(stage, source); err != nil {
		return err
	}
	return os.Rename(stagePath, dst)
}

func copyUpdateBinary(stage *os.File, source io.Reader) error {
	if err := stage.Chmod(0o755); err != nil {
		stage.Close()
		return err
	}
	if _, err := io.Copy(stage, source); err != nil {
		stage.Close()
		return err
	}
	if err := stage.Sync(); err != nil {
		stage.Close()
		return err
	}
	return stage.Close()
}

func sudoStageAndReplaceBinary(src string, dst string) error {
	stage := filepath.Join(filepath.Dir(dst), fmt.Sprintf(".weclaw-update-%d", os.Getpid()))
	defer exec.Command("sudo", "rm", "-f", stage).Run()
	for _, args := range [][]string{{"cp", src, stage}, {"chmod", "755", stage}, {"mv", stage, dst}} {
		cmd := exec.Command("sudo", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func resolveSymlink(path string) (string, error) {
	seen := make(map[string]struct{}, maxSymlinkDepth)
	for depth := 0; depth < maxSymlinkDepth; depth++ {
		if _, ok := seen[path]; ok {
			return "", fmt.Errorf("resolve symlink %s: cycle detected", path)
		}
		seen[path] = struct{}{}
		target, err := os.Readlink(path)
		if err != nil {
			if isReadlinkNonSymlink(err) {
				return path, nil
			}
			return "", err
		}
		if !strings.HasPrefix(target, "/") {
			target = filepath.Join(filepath.Dir(path), target)
		}
		target = filepath.Clean(target)
		path = target
	}
	return "", fmt.Errorf("resolve symlink %s: exceeded max depth %d", path, maxSymlinkDepth)
}

func isReadlinkNonSymlink(err error) bool {
	pathErr, ok := err.(*os.PathError)
	if !ok || pathErr.Err == nil {
		return false
	}
	detail := strings.ToLower(pathErr.Err.Error())
	return strings.Contains(detail, "invalid argument") || strings.Contains(detail, "not a symbolic link")
}
