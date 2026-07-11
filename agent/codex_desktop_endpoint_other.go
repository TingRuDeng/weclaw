//go:build !darwin

package agent

import (
	"context"
	"fmt"
	"net"
)

// dialCodexDesktopEndpoint 在非 Darwin 系统明确拒绝 Desktop IPC。
func dialCodexDesktopEndpoint(context.Context) (net.Conn, error) {
	return nil, fmt.Errorf("%w: 当前系统不支持 Codex Desktop IPC", ErrCodexDesktopUnavailable)
}

// codexDesktopPresence 在非 Darwin 系统明确表示 Desktop 不存在。
func codexDesktopPresence() (bool, bool) {
	return false, false
}
