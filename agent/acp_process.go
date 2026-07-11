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

// Start launches the claude-agent-acp subprocess and initializes the connection.
func (a *ACPAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}

	a.cmd = exec.CommandContext(ctx, a.command, a.args...)
	a.cmd.Dir = a.cwd
	if command, cmdArgs := a.runAs.wrapCommand(a.command, a.args); command != a.command {
		a.cmd = exec.CommandContext(ctx, command, cmdArgs...)
		a.cmd.Dir = a.cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			a.mu.Unlock()
			return fmt.Errorf("build acp env: %w", err)
		}
		a.cmd.Env = cmdEnv
	}
	// Capture stderr for debugging and error reporting
	a.stderr = &acpStderrWriter{prefix: "[acp-stderr]"}
	a.cmd.Stderr = a.stderr

	var err error
	a.stdin, err = a.cmd.StdinPipe()
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := a.cmd.StdoutPipe()
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := a.cmd.Start(); err != nil {
		a.mu.Unlock()
		return fmt.Errorf("start acp agent %s: %w", a.command, err)
	}

	pid := a.cmd.Process.Pid
	log.Printf("[acp] started subprocess (command=%s, pid=%d)", a.command, pid)

	a.scanner = newACPScanner(stdout)
	a.started = true

	// Start reading loop
	go a.readLoop()

	// Release lock before calling initialize — call() needs a.mu to write to stdin
	a.mu.Unlock()

	// Initialize handshake with timeout
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log.Printf("[acp] sending initialize handshake (pid=%d, protocol=%s)...", pid, a.protocol)
	var result json.RawMessage
	if a.protocol == protocolCodexAppServer {
		result, err = a.rpc(initCtx, "initialize", codexInitializeParams())
		if err == nil {
			// codex app-server expects an "initialized" notification after initialize response
			err = a.notify("initialized", nil)
		}
	} else {
		result, err = a.rpc(initCtx, "initialize", initParams{
			ProtocolVersion: 1,
			ClientCapabilities: clientCapabilities{
				FS: &fsCapabilities{ReadTextFile: true, WriteTextFile: true},
			},
		})
	}
	if err != nil {
		a.mu.Lock()
		a.started = false
		stdin := a.stdin
		cmd := a.cmd
		a.stdin = nil
		a.cmd = nil
		a.scanner = nil
		a.mu.Unlock()
		stopACPProcess(stdin, cmd)
		// Use stderr detail if available (e.g. "connect ECONNREFUSED")
		if detail := a.stderr.LastError(); detail != "" {
			return fmt.Errorf("agent startup failed: %s", detail)
		}
		// Provide a helpful hint when the binary looks like a Claude CLI that doesn't support ACP
		base := strings.ToLower(filepath.Base(a.command))
		if base == "claude" || base == "claude.exe" {
			return fmt.Errorf("agent startup failed (pid=%d): %w; claude CLI does not support ACP directly, set type to \"cli\" or install claude-agent-acp", pid, err)
		}
		return fmt.Errorf("agent startup failed (pid=%d): %w", pid, err)
	}

	log.Printf("[acp] initialized (pid=%d): %s", pid, string(result))
	return nil
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
	started := a.started
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
	return a.started
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
