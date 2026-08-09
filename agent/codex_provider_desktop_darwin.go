//go:build darwin

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

const codexDesktopBundleID = "com.openai.codex"

func stopSystemCodexDesktopApp(ctx context.Context) error {
	command := exec.CommandContext(ctx, "osascript", "-e", `tell application id "`+codexDesktopBundleID+`" to quit`)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("请求 Codex App 退出: %w: %s", err, output)
	}
	return waitCodexDesktopPresence(ctx, false)
}

func startSystemCodexDesktopApp(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "open", "-b", codexDesktopBundleID).CombinedOutput(); err != nil {
		return fmt.Errorf("启动 Codex App: %w: %s", err, output)
	}
	return waitCodexDesktopPresence(ctx, true)
}

func waitCodexDesktopPresence(ctx context.Context, wantPresent bool) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		socketExists, processExists := codexDesktopPresence()
		present := socketExists || processExists
		if present == wantPresent {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
