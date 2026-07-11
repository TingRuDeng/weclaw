//go:build darwin

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const codexDesktopProcessName = "Codex"

type codexDesktopEndpointDeps struct {
	lstat func(string) (os.FileInfo, error)
	uid   func() int
	dial  func(context.Context, string) (net.Conn, error)
}

type codexDesktopPresenceDeps struct {
	lstat          func(string) (os.FileInfo, error)
	processRunning func() (bool, error)
}

// codexDesktopEndpointPath 只按当前操作系统用户派生固定 socket 路径。
func codexDesktopEndpointPath() string {
	name := fmt.Sprintf("ipc-%d.sock", os.Getuid())
	return filepath.Join(os.TempDir(), "codex-ipc", name)
}

// dialCodexDesktopEndpoint 使用真实系统依赖连接默认安全 endpoint。
func dialCodexDesktopEndpoint(ctx context.Context) (net.Conn, error) {
	deps := codexDesktopEndpointDeps{
		lstat: os.Lstat,
		uid:   os.Getuid,
		dial:  dialCodexDesktopUnixSocket,
	}
	return dialCodexDesktopEndpointWithDeps(ctx, deps)
}

// dialCodexDesktopEndpointWithDeps 在实际拨号前完成文件类型与归属校验。
func dialCodexDesktopEndpointWithDeps(ctx context.Context, deps codexDesktopEndpointDeps) (net.Conn, error) {
	path := codexDesktopEndpointPath()
	if err := validateCodexDesktopEndpoint(path, deps); err != nil {
		return nil, err
	}
	conn, err := deps.dial(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("%w: 连接 Codex Desktop endpoint: %v", ErrCodexDesktopUnavailable, err)
	}
	return conn, nil
}

// validateCodexDesktopEndpoint 拒绝不存在、符号链接和非 socket endpoint。
func validateCodexDesktopEndpoint(path string, deps codexDesktopEndpointDeps) error {
	info, err := deps.lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: endpoint 不存在", ErrCodexDesktopUnavailable)
		}
		return fmt.Errorf("%w: 检查 endpoint: %v", ErrCodexDesktopUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: endpoint 不是安全的 Unix socket", ErrCodexDesktopUnavailable)
	}
	return validateCodexDesktopEndpointOwner(info, deps.uid())
}

// validateCodexDesktopEndpointOwner 要求 socket 归当前 uid 所有。
func validateCodexDesktopEndpointOwner(info os.FileInfo, wantUID int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != wantUID {
		return fmt.Errorf("%w: endpoint 不属于当前用户", ErrCodexDesktopUnavailable)
	}
	return nil
}

// dialCodexDesktopUnixSocket 建立 Unix domain socket 连接。
func dialCodexDesktopUnixSocket(ctx context.Context, path string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", path)
}

// codexDesktopEndpointAbsent 仅在 socket 与 Codex 主进程都不存在时确认离线。
func codexDesktopEndpointAbsent(path string, deps codexDesktopPresenceDeps) (bool, error) {
	socketPresent, err := codexDesktopPathPresent(path, deps.lstat)
	if err != nil {
		return false, err
	}
	processRunning, err := deps.processRunning()
	if err != nil {
		return false, err
	}
	return !socketPresent && !processRunning, nil
}

// codexDesktopPathPresent 用 Lstat 判断路径是否仍存在。
func codexDesktopPathPresent(path string, lstat func(string) (os.FileInfo, error)) (bool, error) {
	_, err := lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("检查 Codex Desktop endpoint presence: %w", err)
}

// codexDesktopProcessPresent 通过 Darwin sysctl 查询主进程。
func codexDesktopProcessPresent() (bool, error) {
	return codexDesktopProcessPresentFrom(unix.SysctlKinfoProcSlice)
}

// codexDesktopProcessPresentFrom 只接受进程名精确为 Codex 的记录。
func codexDesktopProcessPresentFrom(list func(string, ...int) ([]unix.KinfoProc, error)) (bool, error) {
	processes, err := list("kern.proc.all")
	if err != nil {
		return false, fmt.Errorf("读取 Codex Desktop 进程列表: %w", err)
	}
	for _, process := range processes {
		if codexDesktopProcessCommand(process.Proc.P_comm) == codexDesktopProcessName {
			return true, nil
		}
	}
	return false, nil
}

// codexDesktopProcessCommand 提取内核零结尾进程名。
func codexDesktopProcessCommand(command [17]byte) string {
	bytes := make([]byte, 0, len(command))
	for _, char := range command {
		if char == 0 {
			break
		}
		bytes = append(bytes, char)
	}
	return string(bytes)
}
