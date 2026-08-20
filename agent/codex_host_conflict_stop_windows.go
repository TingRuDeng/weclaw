//go:build windows

package agent

import (
	"context"
	"fmt"
	"time"
)

func stopCodexConflictProcessGroup(context.Context, codexVerifiedHostConflictTarget) error {
	return fmt.Errorf("当前平台不支持停止 Codex Host 进程组")
}

func waitCodexConflictMembersExit(context.Context, []codexHostProcessProof, time.Duration) error {
	return fmt.Errorf("当前平台不支持等待 Codex Host 进程组退出")
}
