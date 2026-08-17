//go:build darwin

package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

func readCodexHostProcessArgs(pid int) ([]string, error) {
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(data) > codexHostSnapshotScanLimit {
		return nil, fmt.Errorf("原始参数超过 %d 字节", codexHostSnapshotScanLimit)
	}
	return parseDarwinCodexHostArgs(data)
}

func parseDarwinCodexHostArgs(data []byte) ([]string, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("kern.procargs2 输出过短")
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	if argc <= 0 || argc > 1<<16 {
		return nil, fmt.Errorf("kern.procargs2 argc=%d 无效", argc)
	}
	cursor := 4
	executableEnd := bytes.IndexByte(data[cursor:], 0)
	if executableEnd < 0 {
		return nil, fmt.Errorf("kern.procargs2 缺少 executable 终止符")
	}
	cursor += executableEnd + 1
	for cursor < len(data) && data[cursor] == 0 {
		cursor++
	}
	args := make([]string, 0, argc)
	for len(args) < argc {
		if cursor >= len(data) {
			return nil, fmt.Errorf("kern.procargs2 argv 不完整")
		}
		argumentEnd := bytes.IndexByte(data[cursor:], 0)
		if argumentEnd < 0 {
			return nil, fmt.Errorf("kern.procargs2 argv 缺少终止符")
		}
		args = append(args, string(data[cursor:cursor+argumentEnd]))
		cursor += argumentEnd + 1
	}
	return args, nil
}
