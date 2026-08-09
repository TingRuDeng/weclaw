//go:build !darwin

package agent

import (
	"context"
	"fmt"
	"runtime"
)

func stopSystemCodexDesktopApp(context.Context) error {
	return fmt.Errorf("当前平台 %s 不支持受控重启 Codex App", runtime.GOOS)
}

func startSystemCodexDesktopApp(context.Context) error {
	return fmt.Errorf("当前平台 %s 不支持受控重启 Codex App", runtime.GOOS)
}
