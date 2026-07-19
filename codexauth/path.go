package codexauth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveCodexHome(agentEnv map[string]string, runAsUser string) (string, error) {
	if value := strings.TrimSpace(agentEnv["CODEX_HOME"]); value != "" {
		return absoluteCleanPath(value)
	}
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return absoluteCleanPath(value)
	}
	if strings.TrimSpace(runAsUser) != "" {
		return "", NewError(CodeInvalid, "run_as_user 使用 Codex 多账号时必须显式配置 CODEX_HOME", nil)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func HostID(codexHome, socketPath string) (string, error) {
	home, err := absoluteCleanPath(codexHome)
	if err != nil {
		return "", err
	}
	socket, err := absoluteCleanPath(socketPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(home + "\x00" + socket))
	return hex.EncodeToString(sum[:])[:20], nil
}

func absoluteCleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return filepath.Clean(absolute), nil
}
