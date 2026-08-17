//go:build !windows

package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func systemCodexHostProcessSnapshot(ctx context.Context, allowedUIDs map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
	cmd := exec.CommandContext(ctx, "ps", "-axww", "-o", "pid=,ppid=,pgid=,uid=,ucomm=,command=")
	stdout := &codexHostSnapshotOutput{limit: codexHostSnapshotScanLimit}
	stderr := &codexHostSnapshotOutput{limit: 4 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("执行只读进程检查: %w", err)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("只读进程检查输出超过 %d 字节", stdout.limit)
	}
	processes, err := parseCodexHostProcessSnapshot(stdout.buffer.String())
	if err != nil {
		return nil, err
	}
	filtered := processes[:0]
	for _, process := range processes {
		if _, allowed := allowedUIDs[process.UID]; !allowed || !codexHostProcessNeedsExactArgs(process) {
			filtered = append(filtered, process)
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		args, err := readCodexHostProcessArgs(process.PID)
		if err != nil {
			if codexHostProcessArgsGone(err) {
				continue
			}
			return nil, fmt.Errorf("读取候选 Codex Host PID %d 原始参数: %w", process.PID, err)
		}
		process.Args = args
		filtered = append(filtered, process)
	}
	return filtered, nil
}

func codexHostProcessNeedsExactArgs(process codexHostProcessSnapshot) bool {
	launcher := strings.ToLower(filepath.Base(strings.TrimSpace(process.Executable)))
	switch launcher {
	case "codex", "codex.exe":
		return true
	case "node", "node.exe":
		_, ok := commandTailAfterExecutable(process.Command, []string{"codex.js", "codex"})
		return ok
	default:
		return false
	}
}

type codexHostSnapshotOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (w *codexHostSnapshotOutput) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		w.overflow = w.overflow || originalLength > 0
		return originalLength, nil
	}
	if len(data) > remaining {
		w.overflow = true
		data = data[:remaining]
	}
	_, _ = w.buffer.Write(data)
	return originalLength, nil
}
