//go:build windows

package agent

import (
	"context"
	"fmt"
)

func systemCodexHostProcessSnapshot(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
	return nil, fmt.Errorf("当前平台暂不支持 Codex Host 进程组只读预检")
}
