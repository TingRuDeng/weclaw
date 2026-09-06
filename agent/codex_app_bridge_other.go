//go:build !darwin

package agent

import (
	"context"
	"fmt"
)

func codexAppSharedHostAvailable() bool { return false }
func codexAppSharedNodePath() string    { return "" }
func validateCodexAppSharedNode(context.Context) error {
	return fmt.Errorf("Codex App shared Host currently requires macOS")
}
func prepareSystemCodexAppShared(context.Context, string, codexAppDaemonEnvironment) error {
	return fmt.Errorf("Codex App shared Host currently requires macOS")
}
func configureSystemCodexAppShared(context.Context, string, codexAppDaemonEnvironment) error {
	return fmt.Errorf("Codex App shared Host currently requires macOS")
}

func restoreSystemCodexAppSharedEnvironment(context.Context, string) error { return nil }
