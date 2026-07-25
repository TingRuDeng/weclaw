package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	codexStateRuntimeUpdateThreshold = 2
	codexCLIAutoUpdateCooldown       = 5 * time.Minute
	codexCLIAutoUpdateTimeout        = 3 * time.Minute
	codexCLICommandOutputLimit       = 32 << 10
	codexStateRuntimeRetryDelay      = 2 * time.Second
)

var (
	// ErrCodexCLIAutoUpdateFailed 让上层停止对确定性的更新失败继续盲重试。
	ErrCodexCLIAutoUpdateFailed = errors.New("Codex CLI 自动更新失败")
	codexCLIVersionPattern      = regexp.MustCompile(`(?m)\bcodex-cli\s+([0-9A-Za-z][0-9A-Za-z.+-]*)\b`)
)

type codexCLIUpdateResult struct {
	Before           string
	After            string
	RuntimeAvailable bool
}

func (a *ACPAgent) waitCodexStateRuntimeRetry(ctx context.Context) error {
	delay := a.codexStateRetryDelay
	if delay <= 0 {
		delay = codexStateRuntimeRetryDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsCodexStateRuntimeError 识别 app-server 在打开共享 CODEX_HOME 状态库前退出的
// 上游错误。该错误可能是短暂 IO，也可能是 Codex App 已升级数据库后的版本不兼容。
func IsCodexStateRuntimeError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "failed to initialize sqlite state runtime") ||
		strings.Contains(text, "failed to initialize state runtime")
}

func (a *ACPAgent) maybeAutoUpdateCodexCLI(ctx context.Context, startErr error) (bool, error) {
	if a.codexAutoUpdate != "incompatible" || !a.usesCodexSharedHost() ||
		!IsCodexStateRuntimeError(startErr) {
		if !IsCodexStateRuntimeError(startErr) {
			a.resetCodexStateRuntimeFailures()
		}
		return false, nil
	}

	a.mu.Lock()
	a.codexStateRuntimeFailures++
	failures := a.codexStateRuntimeFailures
	lastAttempt := a.codexLastAutoUpdateAt
	if failures < codexStateRuntimeUpdateThreshold ||
		(!lastAttempt.IsZero() && time.Since(lastAttempt) < codexCLIAutoUpdateCooldown) {
		a.mu.Unlock()
		return false, nil
	}
	a.mu.Unlock()

	if err := a.requireCodexCLIUpdateIdle(); err != nil {
		return false, fmt.Errorf("%w: %v", ErrCodexCLIAutoUpdateFailed, err)
	}
	a.mu.Lock()
	a.codexLastAutoUpdateAt = time.Now()
	a.mu.Unlock()
	updater := a.codexCLIUpdaterCall
	if updater == nil {
		updater = a.updateCodexCLI
	}
	result, err := updater(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrCodexCLIAutoUpdateFailed, err)
	}
	if result.RuntimeAvailable {
		log.Printf("[codex-update] compatible app-server became available while waiting for update lock")
		a.resetCodexStateRuntimeFailures()
		return true, nil
	}
	if result.Before == "" || result.After == "" {
		return false, fmt.Errorf("%w: 无法验证更新前后的 Codex CLI 版本", ErrCodexCLIAutoUpdateFailed)
	}
	if result.Before == result.After {
		return false, fmt.Errorf(
			"%w: codex update 后版本仍为 %s；当前 Codex App 状态库可能领先于可用 CLI",
			ErrCodexCLIAutoUpdateFailed, result.After,
		)
	}
	log.Printf("[codex-update] Codex CLI updated after repeated state runtime failures (%s -> %s)", result.Before, result.After)
	a.resetCodexStateRuntimeFailures()
	return true, nil
}

func (a *ACPAgent) resetCodexStateRuntimeFailures() {
	a.mu.Lock()
	a.codexStateRuntimeFailures = 0
	a.mu.Unlock()
}

func (a *ACPAgent) requireCodexCLIUpdateIdle() error {
	if a.codexOwners == nil {
		return nil
	}
	count, uncertain := a.codexOwners.anyWriterLeaseStatus()
	if count == 0 {
		return nil
	}
	if uncertain {
		return fmt.Errorf("仍有 %d 个 writer lease（包含未知运行态），拒绝更新", count)
	}
	return fmt.Errorf("仍有 %d 个 writer lease，拒绝更新", count)
}

func (a *ACPAgent) updateCodexCLI(ctx context.Context) (codexCLIUpdateResult, error) {
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return codexCLIUpdateResult{}, err
	}
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return codexCLIUpdateResult{}, err
	}
	lockFile, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return codexCLIUpdateResult{}, fmt.Errorf("等待 Codex Host 生命周期锁: %w", err)
	}
	defer releaseCodexHostStartupLock(lockFile)

	if conn, dialErr := dialCodexHost(ctx, socketPath); dialErr == nil {
		_ = conn.Close()
		return codexCLIUpdateResult{RuntimeAvailable: true}, nil
	}
	if err := a.requireCodexCLIUpdateIdle(); err != nil {
		return codexCLIUpdateResult{}, err
	}

	updateCtx, cancel := context.WithTimeout(ctx, codexCLIAutoUpdateTimeout)
	defer cancel()
	before, err := a.readCodexCLIVersion(updateCtx)
	if err != nil {
		return codexCLIUpdateResult{}, fmt.Errorf("读取当前 Codex CLI 版本: %w", err)
	}
	log.Printf("[codex-update] repeated state runtime failures; running controlled Codex CLI update (version=%s)", before)
	if _, err := a.runCodexCLICommand(updateCtx, "update"); err != nil {
		return codexCLIUpdateResult{}, fmt.Errorf("执行 codex update: %w", err)
	}
	after, err := a.readCodexCLIVersion(updateCtx)
	if err != nil {
		return codexCLIUpdateResult{}, fmt.Errorf("验证更新后的 Codex CLI 版本: %w", err)
	}
	return codexCLIUpdateResult{Before: before, After: after}, nil
}

func (a *ACPAgent) readCodexCLIVersion(ctx context.Context) (string, error) {
	output, err := a.runCodexCLICommand(ctx, "--version")
	if err != nil {
		return "", err
	}
	match := codexCLIVersionPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("命令 %q 未返回可识别的 codex-cli 版本", a.command)
	}
	return match[1], nil
}

func (a *ACPAgent) runCodexCLICommand(ctx context.Context, args ...string) (string, error) {
	command, wrappedArgs := a.runAs.wrapCommand(a.command, args)
	cmd := exec.CommandContext(ctx, command, wrappedArgs...)
	cmd.Dir = a.cwd
	configureACPProcess(cmd)
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return "", fmt.Errorf("构建 Codex CLI 环境: %w", err)
		}
		cmd.Env = cmdEnv
	}
	output := &boundedCommandOutput{limit: codexCLICommandOutputLimit}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return output.String(), err
	}
	return output.String(), nil
}

type boundedCommandOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (w *boundedCommandOutput) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = w.buffer.Write(data)
	}
	return originalLength, nil
}

func (w *boundedCommandOutput) String() string {
	return w.buffer.String()
}
