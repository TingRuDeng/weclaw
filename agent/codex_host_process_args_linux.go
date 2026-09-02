//go:build linux

package agent

import (
	"fmt"
	"os"
	"strconv"
)

func readCodexHostProcessArgs(pid int) (string, []string, error) {
	processPath := "/proc/" + strconv.Itoa(pid)
	executable, err := os.Readlink(processPath + "/exe")
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(processPath + "/cmdline")
	if err != nil {
		return "", nil, err
	}
	if len(data) > codexHostSnapshotScanLimit {
		return "", nil, fmt.Errorf("原始参数超过 %d 字节", codexHostSnapshotScanLimit)
	}
	args, err := parseNullTerminatedCodexHostArgs(data)
	return executable, args, err
}
