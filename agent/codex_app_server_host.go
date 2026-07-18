package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/gorilla/websocket"
)

const (
	codexHostConnectTimeout = 10 * time.Second
	codexHostDialTimeout    = time.Second
	codexHostWriteTimeout   = 10 * time.Second
	codexHostLockPoll       = 25 * time.Millisecond
	// Darwin has the smallest sockaddr_un.sun_path limit among release targets.
	// Keeping a little headroom also makes the error deterministic before dial.
	codexHostSocketMaxBytes = 100
)

// usesCodexSharedHost keeps test-only protocol overrides and legacy ACP wrappers
// on stdio. The shared socket is only valid for the native `codex app-server`.
func (a *ACPAgent) usesCodexSharedHost() bool {
	if a.protocol != protocolCodexAppServer {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.TrimSpace(a.command)))
	return base == "codex" || base == "codex.exe"
}

func (a *ACPAgent) launchCodexHostClient(ctx context.Context) (int, error) {
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return 0, err
	}
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return 0, err
	}

	if conn, dialErr := dialCodexHost(ctx, socketPath); dialErr == nil {
		if err := a.attachCodexHostConnection(conn); err != nil {
			_ = conn.Close()
			return 0, err
		}
		log.Printf("[codex-host] connected to existing app-server (socket=%s)", socketPath)
		return 0, nil
	}

	startupLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return 0, err
	}
	defer releaseCodexHostStartupLock(startupLock)

	// Another frontend may have won the startup race while this process waited
	// for the cross-process lock. Revalidate the path and connect before ever
	// removing a socket or launching another host.
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return 0, err
	}
	if conn, dialErr := dialCodexHost(ctx, socketPath); dialErr == nil {
		if err := a.attachCodexHostConnection(conn); err != nil {
			_ = conn.Close()
			return 0, err
		}
		log.Printf("[codex-host] connected to app-server started by another frontend (socket=%s)", socketPath)
		return 0, nil
	}

	if err := a.removeStaleCodexHostSocket(socketPath); err != nil {
		return 0, err
	}
	cmd, done, err := a.startCodexHostProcess(ctx, socketPath)
	if err != nil {
		return 0, err
	}
	conn, err := waitForCodexHost(ctx, socketPath, done)
	if err != nil {
		stopCodexHostProcess(cmd, done)
		a.clearOwnedCodexHost(cmd)
		if a.stderr != nil {
			if detail := a.stderr.LastError(); detail != "" {
				return 0, fmt.Errorf("%w; stderr: %s", err, detail)
			}
		}
		return 0, err
	}
	if err := a.attachCodexHostConnection(conn); err != nil {
		_ = conn.Close()
		stopCodexHostProcess(cmd, done)
		a.clearOwnedCodexHost(cmd)
		return 0, err
	}
	log.Printf("[codex-host] started shared app-server (socket=%s, pid=%d)", socketPath, cmd.Process.Pid)
	return cmd.Process.Pid, nil
}

// acquireCodexHostStartupLock serializes stale-socket removal and host startup
// across independent WeClaw processes. The lock file stays in the private
// socket directory; kernel flock ownership is released automatically if a
// process exits unexpectedly.
func (a *ACPAgent) acquireCodexHostStartupLock(ctx context.Context, socketPath string) (*os.File, error) {
	lockPath := socketPath + ".lock"
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open codex app-server startup lock: %w", err)
	}
	lockFile := os.NewFile(uintptr(fd), lockPath)
	if lockFile == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("open codex app-server startup lock: invalid file descriptor")
	}
	closeWithError := func(err error) (*os.File, error) {
		_ = lockFile.Close()
		return nil, err
	}

	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return closeWithError(fmt.Errorf("inspect codex app-server startup lock: %w", err))
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return closeWithError(fmt.Errorf("codex app-server startup lock must be a regular file: %s", lockPath))
	}
	if stat.Mode&0o077 != 0 {
		return closeWithError(fmt.Errorf("codex app-server startup lock must not be accessible by group or others: %s", lockPath))
	}
	if _, ok := a.allowedCodexHostUIDs()[stat.Uid]; !ok {
		return closeWithError(fmt.Errorf("refusing codex app-server startup lock owned by uid %d: %s", stat.Uid, lockPath))
	}

	ticker := time.NewTicker(codexHostLockPoll)
	defer ticker.Stop()
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lockFile, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return closeWithError(fmt.Errorf("lock codex app-server startup: %w", err))
		}
		select {
		case <-ctx.Done():
			return closeWithError(fmt.Errorf("wait for codex app-server startup lock: %w", ctx.Err()))
		case <-ticker.C:
		}
	}
}

func releaseCodexHostStartupLock(lockFile *os.File) {
	if lockFile == nil {
		return
	}
	_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	_ = lockFile.Close()
}

func (a *ACPAgent) resolveCodexHostSocket() (string, error) {
	configured := strings.TrimSpace(a.codexHostSocket)
	useDefault := configured == ""
	if configured == "" {
		if a.runAs.shouldIsolate() {
			return "", fmt.Errorf("codex app-server shared socket requires app_server_socket when run_as_user is enabled")
		}
		dataDir, err := config.DataDir()
		if err != nil {
			return "", fmt.Errorf("resolve WeClaw data directory: %w", err)
		}
		configured = filepath.Join(dataDir, "runtime", "codex-app-server.sock")
	}
	abs, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("resolve codex app-server socket: %w", err)
	}
	abs = filepath.Clean(abs)
	if len([]byte(abs)) > codexHostSocketMaxBytes {
		if !useDefault {
			return "", fmt.Errorf("codex app-server socket path is too long (%d bytes, max %d): %s", len([]byte(abs)), codexHostSocketMaxBytes, abs)
		}
		abs = fallbackCodexHostSocket(abs)
	}
	a.mu.Lock()
	a.codexHostSocket = abs
	a.mu.Unlock()
	return abs, nil
}

// fallbackCodexHostSocket keeps the address deterministic while avoiding the
// small sockaddr_un path limit on macOS. The per-user directory remains private.
func fallbackCodexHostSocket(intendedPath string) string {
	digest := sha256.Sum256([]byte(intendedPath))
	tempRoot := filepath.Join(string(filepath.Separator), "tmp")
	// Codex rejects a Unix socket whose directory chain contains a symlink.
	// On macOS /tmp points to /private/tmp, so resolve the stable system temp
	// root before creating the private per-user fallback directory.
	if resolved, err := filepath.EvalSymlinks(tempRoot); err == nil && filepath.IsAbs(resolved) {
		tempRoot = resolved
	}
	return filepath.Join(
		tempRoot, fmt.Sprintf("weclaw-%d", os.Geteuid()),
		fmt.Sprintf("codex-%x.sock", digest[:8]),
	)
}

func (a *ACPAgent) prepareCodexHostSocket(socketPath string) error {
	parent := filepath.Dir(socketPath)
	if a.runAs.shouldIsolate() {
	} else {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create codex app-server socket directory: %w", err)
		}
	}
	if err := validateCodexHostDirectory(parent, a.allowedCodexHostDirectoryUIDs()); err != nil {
		return err
	}
	return validateExistingCodexHostSocket(socketPath, a.allowedCodexHostUIDs())
}

func (a *ACPAgent) allowedCodexHostDirectoryUIDs() map[uint32]struct{} {
	if !a.runAs.shouldIsolate() {
		return map[uint32]struct{}{uint32(os.Geteuid()): {}}
	}
	target, err := user.Lookup(strings.TrimSpace(a.runAs.User))
	if err != nil {
		return map[uint32]struct{}{}
	}
	uid, err := strconv.ParseUint(target.Uid, 10, 32)
	if err != nil {
		return map[uint32]struct{}{}
	}
	return map[uint32]struct{}{uint32(uid): {}}
}

func validateCodexHostDirectory(parent string, allowedUIDs map[uint32]struct{}) error {
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect codex app-server socket directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("codex app-server socket parent must be a real directory: %s", parent)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("codex app-server socket directory must not be accessible by group or others: %s", parent)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect codex app-server socket directory owner: unsupported file metadata")
	}
	if _, ok := allowedUIDs[stat.Uid]; !ok {
		return fmt.Errorf("refusing codex app-server socket directory owned by uid %d: %s", stat.Uid, parent)
	}
	return nil
}

func (a *ACPAgent) allowedCodexHostUIDs() map[uint32]struct{} {
	allowed := map[uint32]struct{}{uint32(os.Geteuid()): {}}
	if !a.runAs.shouldIsolate() {
		return allowed
	}
	target, err := user.Lookup(strings.TrimSpace(a.runAs.User))
	if err != nil {
		return allowed
	}
	uid, err := strconv.ParseUint(target.Uid, 10, 32)
	if err == nil {
		allowed[uint32(uid)] = struct{}{}
	}
	return allowed
}

func validateExistingCodexHostSocket(socketPath string, allowedUIDs map[uint32]struct{}) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect codex app-server socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing non-socket codex app-server path: %s", socketPath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect codex app-server socket owner: unsupported file metadata")
	}
	if _, ok := allowedUIDs[stat.Uid]; !ok {
		return fmt.Errorf("refusing codex app-server socket owned by uid %d: %s", stat.Uid, socketPath)
	}
	return nil
}

func (a *ACPAgent) removeStaleCodexHostSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect stale codex app-server socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket codex app-server path: %s", socketPath)
	}
	if err := validateExistingCodexHostSocket(socketPath, a.allowedCodexHostUIDs()); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale codex app-server socket: %w", err)
	}
	return nil
}

func dialCodexHost(ctx context.Context, socketPath string) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, codexHostDialTimeout)
	defer cancel()
	netDialer := &net.Dialer{}
	dialer := websocket.Dialer{
		HandshakeTimeout: codexHostDialTimeout,
		NetDialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return netDialer.DialContext(dialCtx, "unix", socketPath)
		},
	}
	conn, response, err := dialer.DialContext(dialCtx, "ws://localhost/rpc", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect codex app-server websocket: %w", err)
	}
	return conn, nil
}

func waitForCodexHost(ctx context.Context, socketPath string, done <-chan error) (*websocket.Conn, error) {
	waitCtx, cancel := context.WithTimeout(ctx, codexHostConnectTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if conn, err := dialCodexHost(waitCtx, socketPath); err == nil {
			return conn, nil
		}
		select {
		case err, ok := <-done:
			if !ok || err == nil {
				err = fmt.Errorf("process exited")
			}
			return nil, fmt.Errorf("codex app-server exited before socket became ready: %w", err)
		case <-waitCtx.Done():
			return nil, fmt.Errorf("wait for codex app-server socket: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (a *ACPAgent) startCodexHostProcess(ctx context.Context, socketPath string) (*exec.Cmd, <-chan error, error) {
	args := codexSharedHostArgs(a.args, socketPath)
	command, args := a.runAs.wrapCommand(a.command, args)
	// The host outlives the request that happened to start it; its lifecycle is
	// controlled explicitly by the owning ACPAgent, not by a frontend context.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), command, args...)
	cmd.Dir = a.cwd
	configureACPProcess(cmd)
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return nil, nil, fmt.Errorf("build codex app-server env: %w", err)
		}
		cmd.Env = cmdEnv
	}
	a.stderr = &acpStderrWriter{prefix: "[codex-host]"}
	cmd.Stderr = a.stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start codex app-server host %s: %w", a.command, err)
	}
	done := make(chan error, 1)
	a.mu.Lock()
	a.hostCmd = cmd
	a.hostDone = done
	a.mu.Unlock()
	go a.waitCodexHostProcess(cmd, done)
	return cmd, done, nil
}

func codexSharedHostArgs(args []string, socketPath string) []string {
	result := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--listen" && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--listen=") {
			continue
		}
		result = append(result, arg)
	}
	result = append(result, "--listen", "unix://"+socketPath)
	return result
}

func (a *ACPAgent) waitCodexHostProcess(cmd *exec.Cmd, done chan<- error) {
	err := cmd.Wait()
	done <- err
	close(done)
	a.clearOwnedCodexHost(cmd)
	if err != nil {
		log.Printf("[codex-host] app-server exited (pid=%d): %v", cmd.Process.Pid, err)
	} else {
		log.Printf("[codex-host] app-server exited (pid=%d)", cmd.Process.Pid)
	}
}

func (a *ACPAgent) clearOwnedCodexHost(cmd *exec.Cmd) {
	a.mu.Lock()
	if a.hostCmd == cmd {
		a.hostCmd = nil
		a.hostDone = nil
	}
	a.mu.Unlock()
}

func (a *ACPAgent) attachCodexHostConnection(conn *websocket.Conn) error {
	if conn == nil {
		return fmt.Errorf("codex app-server connection is nil")
	}
	transport := newCodexWebSocketTransport(conn)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin != nil || a.scanner != nil || a.started {
		_ = transport.Close()
		return fmt.Errorf("codex app-server client is already connected")
	}
	a.stdin = transport
	a.scanner = newACPScanner(transport)
	a.started = true
	go a.readLoop()
	return nil
}

// codexWebSocketTransport adapts the app-server's WebSocket-over-UDS protocol
// to the existing newline-oriented JSON-RPC reader/writer. The upstream Unix
// socket is not a raw JSONL stream: every request and response is one WebSocket
// frame after a standard HTTP Upgrade handshake.
type codexWebSocketTransport struct {
	conn       *websocket.Conn
	reader     *io.PipeReader
	pipeWriter *io.PipeWriter
	writeMu    sync.Mutex
	writeBuf   bytes.Buffer
	closed     atomic.Bool
	closeOnce  sync.Once
}

func newCodexWebSocketTransport(conn *websocket.Conn) *codexWebSocketTransport {
	reader, writer := io.Pipe()
	transport := &codexWebSocketTransport{
		conn:       conn,
		reader:     reader,
		pipeWriter: writer,
	}
	go transport.readMessages()
	return transport
}

func (t *codexWebSocketTransport) Read(data []byte) (int, error) {
	return t.reader.Read(data)
}

func (t *codexWebSocketTransport) Write(data []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	_, _ = t.writeBuf.Write(data)
	deadlineSet := false
	for {
		buffered := t.writeBuf.Bytes()
		newline := bytes.IndexByte(buffered, '\n')
		if newline < 0 {
			break
		}
		message := bytes.TrimSuffix(buffered[:newline], []byte{'\r'})
		if len(message) > 0 {
			if !deadlineSet {
				if err := t.conn.SetWriteDeadline(time.Now().Add(codexHostWriteTimeout)); err != nil {
					_ = t.closeWithError(err)
					return len(data), err
				}
				deadlineSet = true
			}
			if err := t.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				_ = t.closeWithError(err)
				return len(data), err
			}
		}
		t.writeBuf.Next(newline + 1)
	}
	if deadlineSet {
		if err := t.conn.SetWriteDeadline(time.Time{}); err != nil {
			_ = t.closeWithError(err)
			return len(data), err
		}
	}
	return len(data), nil
}

func (t *codexWebSocketTransport) Close() error {
	return t.closeWithError(nil)
}

func (t *codexWebSocketTransport) readMessages() {
	for {
		messageType, message, err := t.conn.ReadMessage()
		if err != nil {
			_ = t.closeWithError(err)
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if _, err := t.pipeWriter.Write(message); err != nil {
			_ = t.closeWithError(err)
			return
		}
		if _, err := t.pipeWriter.Write([]byte{'\n'}); err != nil {
			_ = t.closeWithError(err)
			return
		}
	}
}

func (t *codexWebSocketTransport) closeWithError(streamErr error) error {
	var closeErr error
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		// Gorilla permits Close concurrently with the single reader and writer.
		// Closing before waiting on writeMu unblocks an in-flight local socket
		// write instead of making shutdown depend on the peer draining frames.
		closeErr = t.conn.Close()
		if streamErr != nil {
			_ = t.pipeWriter.CloseWithError(streamErr)
		} else {
			_ = t.pipeWriter.Close()
		}
	})
	return closeErr
}

func (a *ACPAgent) disconnectCodexHostClient(stopHost bool) (io.Closer, *exec.Cmd, <-chan error) {
	a.mu.Lock()
	connection := a.stdin
	a.started = false
	a.stdin = nil
	a.scanner = nil
	var cmd *exec.Cmd
	var done <-chan error
	if stopHost {
		cmd, done = a.hostCmd, a.hostDone
		a.hostCmd, a.hostDone = nil, nil
	}
	a.mu.Unlock()
	return connection, cmd, done
}

func stopCodexHostProcess(cmd *exec.Cmd, done <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if cmd.Cancel != nil {
		_ = cmd.Cancel()
	} else {
		_ = cmd.Process.Kill()
	}
	if done == nil {
		return
	}
	timer := time.NewTimer(acpKillGrace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		sweepProcessGroup(cmd)
		_ = cmd.Process.Kill()
	}
	select {
	case <-done:
	case <-time.After(acpKillGrace):
	}
}
