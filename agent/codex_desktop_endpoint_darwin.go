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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	codexDesktopCurrentProcessName = "ChatGPT"
	codexDesktopLegacyProcessName  = "Codex"
	codexDesktopPresenceTimeout    = 250 * time.Millisecond
)

var errCodexDesktopEndpointUnsafe = errors.New("Codex Desktop endpoint 不安全")

func newSystemCodexDesktopRuntime() *codexDesktopRuntime {
	return newCodexDesktopRuntime()
}

type codexDesktopEndpointDeps struct {
	candidates func() ([]codexDesktopEndpointCandidate, error)
	lstat      func(string) (os.FileInfo, error)
	uid        func() int
	dial       func(context.Context, string) (net.Conn, error)
}

type codexDesktopPresenceDeps struct {
	probe          func(context.Context) (bool, error)
	processRunning func() (bool, error)
	timeout        time.Duration
}

type codexDesktopEndpointCandidate struct {
	name              string
	path              string
	strictPermissions bool
}

func codexDesktopCurrentEndpointPath() (string, error) {
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析 Codex Desktop home: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return "", fmt.Errorf("Codex Desktop CODEX_HOME 必须是绝对路径")
	}
	return filepath.Join(filepath.Clean(codexHome), "ipc", "ipc.sock"), nil
}

func codexDesktopLegacyEndpointPath() string {
	name := fmt.Sprintf("ipc-%d.sock", os.Getuid())
	return filepath.Join(os.TempDir(), "codex-ipc", name)
}

func codexDesktopEndpointCandidates() ([]codexDesktopEndpointCandidate, error) {
	current, err := codexDesktopCurrentEndpointPath()
	if err != nil {
		return nil, err
	}
	return []codexDesktopEndpointCandidate{
		{name: "current", path: current, strictPermissions: true},
		{name: "legacy", path: codexDesktopLegacyEndpointPath()},
	}, nil
}

func systemCodexDesktopEndpointDeps() codexDesktopEndpointDeps {
	return codexDesktopEndpointDeps{
		candidates: codexDesktopEndpointCandidates,
		lstat:      os.Lstat,
		uid:        os.Getuid,
		dial:       dialCodexDesktopUnixSocket,
	}
}

func (deps codexDesktopEndpointDeps) withDefaults() codexDesktopEndpointDeps {
	if deps.candidates == nil {
		deps.candidates = codexDesktopEndpointCandidates
	}
	if deps.lstat == nil {
		deps.lstat = os.Lstat
	}
	if deps.uid == nil {
		deps.uid = os.Getuid
	}
	if deps.dial == nil {
		deps.dial = dialCodexDesktopUnixSocket
	}
	return deps
}

// dialCodexDesktopEndpoint 使用真实系统依赖连接默认安全 endpoint。
func dialCodexDesktopEndpoint(ctx context.Context) (net.Conn, error) {
	return dialCodexDesktopEndpointWithDeps(ctx, systemCodexDesktopEndpointDeps())
}

// dialCodexDesktopEndpointWithDeps 优先连接当前 App endpoint，旧地址仅作安全兼容回退。
func dialCodexDesktopEndpointWithDeps(ctx context.Context, deps codexDesktopEndpointDeps) (net.Conn, error) {
	deps = deps.withDefaults()
	candidates, err := deps.candidates()
	if err != nil {
		return nil, fmt.Errorf("%w: 解析 endpoint: %v", ErrCodexDesktopUnavailable, err)
	}
	var failures []error
	for _, candidate := range candidates {
		if err := validateCodexDesktopEndpointCandidate(candidate, deps); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		conn, err := deps.dial(ctx, candidate.path)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: 连接 endpoint: %v", ErrCodexDesktopUnavailable, ctx.Err())
		}
		failures = append(failures, fmt.Errorf("%s endpoint %s: %w", candidate.name, candidate.path, err))
	}
	if len(failures) == 0 {
		return nil, fmt.Errorf("%w: endpoint 不存在", ErrCodexDesktopUnavailable)
	}
	return nil, fmt.Errorf("%w: 没有可连接的安全 endpoint: %v", ErrCodexDesktopUnavailable, errors.Join(failures...))
}

// validateCodexDesktopEndpoint 拒绝不存在、符号链接和非 socket endpoint。
func validateCodexDesktopEndpoint(path string, deps codexDesktopEndpointDeps) error {
	deps = deps.withDefaults()
	info, err := deps.lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: endpoint 不存在: %w", ErrCodexDesktopUnavailable, fs.ErrNotExist)
		}
		return fmt.Errorf("%w: 检查 endpoint: %v", ErrCodexDesktopUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return codexDesktopUnsafeEndpointError("endpoint 不是 Unix socket")
	}
	return validateCodexDesktopEndpointOwner(info, deps.uid())
}

func validateCodexDesktopEndpointCandidate(candidate codexDesktopEndpointCandidate, deps codexDesktopEndpointDeps) error {
	deps = deps.withDefaults()
	if err := validateCodexDesktopEndpoint(candidate.path, deps); err != nil {
		return err
	}
	parent := filepath.Dir(candidate.path)
	info, err := deps.lstat(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: endpoint 目录不存在: %w", ErrCodexDesktopUnavailable, fs.ErrNotExist)
		}
		return fmt.Errorf("%w: 检查 endpoint 目录: %v", ErrCodexDesktopUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return codexDesktopUnsafeEndpointError("endpoint 父目录不是实体目录")
	}
	if err := validateCodexDesktopEndpointOwner(info, deps.uid()); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return codexDesktopUnsafeEndpointError("endpoint 父目录允许其他用户写入")
	}
	endpointInfo, err := deps.lstat(candidate.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: endpoint 不存在: %w", ErrCodexDesktopUnavailable, fs.ErrNotExist)
		}
		return fmt.Errorf("%w: 复核 endpoint: %v", ErrCodexDesktopUnavailable, err)
	}
	if endpointInfo.Mode()&os.ModeSymlink != 0 || endpointInfo.Mode()&os.ModeSocket == 0 {
		return codexDesktopUnsafeEndpointError("endpoint 复核时不再是 Unix socket")
	}
	if err := validateCodexDesktopEndpointOwner(endpointInfo, deps.uid()); err != nil {
		return err
	}
	if candidate.strictPermissions && (info.Mode().Perm() != 0o700 || endpointInfo.Mode().Perm() != 0o600) {
		return codexDesktopUnsafeEndpointError("当前 endpoint 必须使用 0700 目录和 0600 socket")
	}
	return nil
}

func codexDesktopUnsafeEndpointError(message string) error {
	return fmt.Errorf("%w: %w: %s", ErrCodexDesktopUnavailable, errCodexDesktopEndpointUnsafe, message)
}

// validateCodexDesktopEndpointOwner 要求 socket 归当前 uid 所有。
func validateCodexDesktopEndpointOwner(info os.FileInfo, wantUID int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != wantUID {
		return codexDesktopUnsafeEndpointError("endpoint 不属于当前用户")
	}
	return nil
}

// dialCodexDesktopUnixSocket 建立 Unix domain socket 连接。
func dialCodexDesktopUnixSocket(ctx context.Context, path string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", path)
}

// codexDesktopEndpointConnectableWithDeps 只把能建立连接的安全 socket 视为在线。
func codexDesktopEndpointConnectableWithDeps(ctx context.Context, deps codexDesktopEndpointDeps) (bool, error) {
	deps = deps.withDefaults()
	candidates, err := deps.candidates()
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		if err := validateCodexDesktopEndpointCandidate(candidate, deps); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return false, err
		}
		conn, err := deps.dial(ctx, candidate.path)
		if err == nil {
			_ = conn.Close()
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			continue
		}
		return false, fmt.Errorf("探测 Codex Desktop endpoint: %w", err)
	}
	return false, nil
}

// codexDesktopProcessPresent 通过 Darwin sysctl 查询主进程。
func codexDesktopProcessPresent() (bool, error) {
	return codexDesktopProcessPresentFrom(unix.SysctlKinfoProcSlice)
}

// codexDesktopPresence 返回可连接 socket/主进程存在性，探测错误时保持保守占用。
func codexDesktopPresence() (bool, bool) {
	endpointDeps := systemCodexDesktopEndpointDeps()
	return codexDesktopPresenceWithDeps(codexDesktopPresenceDeps{
		probe: func(ctx context.Context) (bool, error) {
			return codexDesktopEndpointConnectableWithDeps(ctx, endpointDeps)
		},
		processRunning: codexDesktopProcessPresent,
		timeout:        codexDesktopPresenceTimeout,
	})
}

func codexDesktopPresenceWithDeps(deps codexDesktopPresenceDeps) (bool, bool) {
	timeout := deps.timeout
	if timeout <= 0 {
		timeout = codexDesktopPresenceTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	socketPresent, socketErr := deps.probe(ctx)
	processPresent, processErr := deps.processRunning()
	if socketErr != nil || processErr != nil {
		return true, true
	}
	return socketPresent, processPresent
}

// codexDesktopProcessPresentFrom 兼容旧 Codex 与当前 ChatGPT 主进程名。
func codexDesktopProcessPresentFrom(list func(string, ...int) ([]unix.KinfoProc, error)) (bool, error) {
	processes, err := list("kern.proc.all")
	if err != nil {
		return false, fmt.Errorf("读取 Codex Desktop 进程列表: %w", err)
	}
	for _, process := range processes {
		if int(process.Eproc.Ucred.Uid) != os.Getuid() {
			continue
		}
		if codexDesktopProcessName(codexDesktopProcessCommand(process.Proc.P_comm)) {
			return true, nil
		}
	}
	return false, nil
}

func codexDesktopProcessName(name string) bool {
	return name == codexDesktopCurrentProcessName || name == codexDesktopLegacyProcessName
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
