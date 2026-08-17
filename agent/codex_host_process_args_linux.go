//go:build linux

package agent

import (
	"fmt"
	"os"
	"strconv"
)

func readCodexHostProcessArgs(pid int) ([]string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	if len(data) > codexHostSnapshotScanLimit {
		return nil, fmt.Errorf("原始参数超过 %d 字节", codexHostSnapshotScanLimit)
	}
	return parseNullTerminatedCodexHostArgs(data)
}
