package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Start launches the ACP subprocess；并发调用会等待同一次初始化结果。
func (a *ACPAgent) Start(ctx context.Context) (err error) {
	leader, done := a.beginACPStart()
	if !leader {
		if done == nil {
			return nil
		}
		return a.waitACPStart(ctx, done)
	}
	defer func() {
		err = a.finishACPStart(err)
	}()
	return a.startACPProcess(ctx)
}

func (a *ACPAgent) startACPProcess(ctx context.Context) error {
	pid, err := a.launchACPSubprocess(ctx)
	if err != nil {
		return err
	}
	_, err = a.initializeACPSubprocess(ctx, pid)
	if err != nil {
		return a.failACPStartup(pid, err)
	}
	log.Printf("[acp] initialized (pid=%d, protocol=%s)", pid, a.protocol)
	return nil
}

func (a *ACPAgent) launchACPSubprocess(ctx context.Context) (int, error) {
	a.mu.Lock()
	cmd := exec.CommandContext(ctx, a.command, a.args...)
	cmd.Dir = a.cwd
	if command, cmdArgs := a.runAs.wrapCommand(a.command, a.args); command != a.command {
		cmd = exec.CommandContext(ctx, command, cmdArgs...)
		cmd.Dir = a.cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			a.mu.Unlock()
			return 0, fmt.Errorf("build acp env: %w", err)
		}
		cmd.Env = cmdEnv
	}
	a.stderr = &acpStderrWriter{prefix: "[acp-stderr]"}
	cmd.Stderr = a.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		a.mu.Unlock()
		return 0, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		a.mu.Unlock()
		return 0, fmt.Errorf("create stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		a.mu.Unlock()
		return 0, fmt.Errorf("start acp agent %s: %w", a.command, err)
	}
	a.cmd = cmd
	a.stdin = stdin
	a.scanner = newACPScanner(stdout)
	a.started = true
	pid := cmd.Process.Pid
	a.mu.Unlock()
	go a.readLoop()
	log.Printf("[acp] started subprocess (command=%s, pid=%d)", a.command, pid)
	return pid, nil
}

func (a *ACPAgent) initializeACPSubprocess(ctx context.Context, pid int) (json.RawMessage, error) {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	log.Printf("[acp] sending initialize handshake (pid=%d, protocol=%s)...", pid, a.protocol)
	if a.protocol == protocolCodexAppServer {
		result, err := a.rpc(initCtx, "initialize", codexInitializeParams())
		if err != nil {
			return nil, err
		}
		return result, a.notify("initialized", nil)
	}
	result, err := a.rpc(initCtx, "initialize", initParams{
		ProtocolVersion: acpProtocolVersion,
		ClientCapabilities: clientCapabilities{
			FS: &fsCapabilities{ReadTextFile: true, WriteTextFile: true},
		},
	})
	if err != nil {
		return nil, err
	}
	return result, a.cacheAndValidateACPCapabilities(result)
}

func (a *ACPAgent) failACPStartup(pid int, startErr error) error {
	a.mu.Lock()
	a.started = false
	stdin := a.stdin
	cmd := a.cmd
	a.stdin = nil
	a.cmd = nil
	a.scanner = nil
	a.mu.Unlock()
	stopACPProcess(stdin, cmd)
	if detail := a.stderr.LastError(); detail != "" {
		return fmt.Errorf("agent startup failed (pid=%d): %w; stderr: %s", pid, startErr, detail)
	}
	base := strings.ToLower(filepath.Base(a.command))
	if base == "claude" || base == "claude.exe" {
		return fmt.Errorf("agent startup failed (pid=%d): %w; claude CLI does not support ACP directly, set type to \"cli\" or install claude-agent-acp", pid, startErr)
	}
	return fmt.Errorf("agent startup failed (pid=%d): %w", pid, startErr)
}

func (a *ACPAgent) beginACPStart() (bool, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.starting {
		return false, a.startDone
	}
	if a.started {
		return false, nil
	}
	a.starting = true
	a.startDone = make(chan struct{})
	a.startErr = nil
	return true, a.startDone
}

func (a *ACPAgent) waitACPStart(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.startErr
	}
}

func (a *ACPAgent) finishACPStart(startErr error) error {
	a.mu.Lock()
	if startErr == nil && !a.started {
		startErr = fmt.Errorf("ACP runtime exited during startup")
	}
	a.starting = false
	a.startErr = startErr
	done := a.startDone
	a.startDone = nil
	a.mu.Unlock()
	if done != nil {
		close(done)
	}
	return startErr
}

// stopACPProcess 关闭 ACP 子进程资源；启动失败和显式 Stop 都必须容忍 readLoop 已经清理状态。
func stopACPProcess(stdin io.Closer, cmd *exec.Cmd) {
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func codexInitializeParams() map[string]interface{} {
	return map[string]interface{}{
		"clientInfo": map[string]string{"name": "weclaw", "version": "0.3.0"},
		"capabilities": map[string]interface{}{
			"experimentalApi": true,
		},
	}
}

// newACPScanner 创建 ACP stdout 读取器；Codex MCP 启动状态可能输出较大的单行 JSON。
func newACPScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, acpScannerInitialBufferSize), acpScannerMaxTokenSize)
	return scanner
}

// Stop terminates the subprocess.
func (a *ACPAgent) Stop() {
	a.mu.Lock()
	if !a.started && a.stdin == nil && a.cmd == nil {
		a.mu.Unlock()
		return
	}
	stdin := a.stdin
	cmd := a.cmd
	a.started = false
	a.stdin = nil
	a.cmd = nil
	a.scanner = nil
	a.mu.Unlock()

	stopACPProcess(stdin, cmd)
	a.failRuntimeWaiters("ACP runtime stopped")
}

// ensureStarted 确保重置会话前有可写的真实 runtime；测试桩直接走 rpcCall。
func (a *ACPAgent) ensureStarted(ctx context.Context) error {
	if a.rpcCall != nil {
		return nil
	}
	a.mu.Lock()
	started := a.started && !a.starting
	a.mu.Unlock()
	if started {
		return nil
	}
	return a.Start(ctx)
}

// isRuntimeStarted 在锁内读取 ACP 运行时状态，避免 readLoop 清理状态时并发读写。
func (a *ACPAgent) isRuntimeStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started && !a.starting
}

// runtimePID 返回当前子进程 PID；运行时已退出时返回 0 供日志使用。
func (a *ACPAgent) runtimePID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return 0
	}
	return a.cmd.Process.Pid
}
