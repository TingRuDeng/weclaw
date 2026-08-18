//go:build darwin

package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

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
	args, _, err := parseDarwinProcessArgs(data)
	return args, err
}

func parseDarwinProcessArgs(data []byte) ([]string, int, error) {
	if len(data) < 4 {
		return nil, 0, fmt.Errorf("kern.procargs2 输出过短")
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	if argc <= 0 || argc > 1<<16 {
		return nil, 0, fmt.Errorf("kern.procargs2 argc=%d 无效", argc)
	}
	cursor := 4
	executableEnd := bytes.IndexByte(data[cursor:], 0)
	if executableEnd < 0 {
		return nil, 0, fmt.Errorf("kern.procargs2 缺少 executable 终止符")
	}
	cursor += executableEnd + 1
	for cursor < len(data) && data[cursor] == 0 {
		cursor++
	}
	args := make([]string, 0, argc)
	for len(args) < argc {
		if cursor >= len(data) {
			return nil, 0, fmt.Errorf("kern.procargs2 argv 不完整")
		}
		argumentEnd := bytes.IndexByte(data[cursor:], 0)
		if argumentEnd < 0 {
			return nil, 0, fmt.Errorf("kern.procargs2 argv 缺少终止符")
		}
		args = append(args, string(data[cursor:cursor+argumentEnd]))
		cursor += argumentEnd + 1
	}
	return args, cursor, nil
}

// parseDarwinProcessEnvironmentValue returns only the requested variable so
// callers never expose the rest of a process environment in logs or errors.
func parseDarwinProcessEnvironmentValue(data []byte, name string) (string, bool, error) {
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return "", false, fmt.Errorf("invalid process environment variable name")
	}
	_, cursor, err := parseDarwinProcessArgs(data)
	if err != nil {
		return "", false, err
	}
	prefix := name + "="
	for cursor < len(data) {
		for cursor < len(data) && data[cursor] == 0 {
			cursor++
		}
		if cursor >= len(data) {
			break
		}
		entryEnd := bytes.IndexByte(data[cursor:], 0)
		if entryEnd < 0 {
			return "", false, fmt.Errorf("kern.procargs2 environment 缺少终止符")
		}
		entry := string(data[cursor : cursor+entryEnd])
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true, nil
		}
		cursor += entryEnd + 1
	}
	return "", false, nil
}
