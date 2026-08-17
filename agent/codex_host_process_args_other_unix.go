//go:build !darwin && !linux && !windows

package agent

import "fmt"

func readCodexHostProcessArgs(int) ([]string, error) {
	return nil, fmt.Errorf("当前 Unix 平台暂不支持读取 Codex Host 原始参数")
}
