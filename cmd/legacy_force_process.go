package cmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type legacyProcessIdentity struct {
	pid, uid, pgid int
	executable     string
	startedAt      time.Time
	fingerprint    [32]byte
}

type legacyServiceTarget struct {
	state    runtimeState
	identity legacyProcessIdentity
	systemd  bool
}

func legacyArgsFingerprint(args []string) [32]byte {
	return sha256.Sum256([]byte(strings.Join(args, "\x00")))
}

func captureLegacyService(state runtimeState) (*legacyServiceTarget, error) {
	current, err := readRuntimeState()
	if err != nil {
		return nil, err
	}
	if current != state {
		return nil, fmt.Errorf("旧服务身份已变化，请重新执行 --force")
	}
	if !runtimeLockBusy() {
		return nil, fmt.Errorf("旧服务已退出，请重新执行 --force")
	}
	identity, args, err := inspectLegacyProcess(state.PID)
	if err != nil {
		return nil, fmt.Errorf("读取旧服务进程身份: %w", err)
	}
	if err := validateLegacyServiceIdentity(state, identity, args); err != nil {
		return nil, err
	}
	systemd, err := legacyServiceUsesSystemd(state, identity)
	if err != nil {
		return nil, err
	}
	return &legacyServiceTarget{state: state, identity: identity, systemd: systemd}, nil
}

func validateLegacyServiceIdentity(state runtimeState, identity legacyProcessIdentity, args []string) error {
	if state.PID <= 1 || state.PID == os.Getpid() || state.PID != identity.pid || identity.uid != os.Geteuid() || identity.pgid <= 0 {
		return fmt.Errorf("旧服务 PID/UID/进程组身份无法确认")
	}
	executable := strings.TrimSuffix(identity.executable, " (deleted)")
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	expected := filepath.Clean(state.Exe)
	if resolved, err := filepath.EvalSymlinks(expected); err == nil {
		expected = resolved
	}
	if !filepath.IsAbs(state.Exe) || filepath.Clean(executable) != expected || state.StartedAt.IsZero() || identity.startedAt.IsZero() {
		return fmt.Errorf("旧服务可执行文件或启动时间无法确认")
	}
	// 状态文件在进程取得运行锁后写入，允许系统启动时间的秒级精度。
	if identity.startedAt.After(state.StartedAt.Add(time.Second)) {
		return fmt.Errorf("旧服务 PID 启动时间与运行记录不一致")
	}
	if len(args) < 2 || args[1] != "start" {
		return fmt.Errorf("目标进程不是 WeClaw start 服务")
	}
	return nil
}

func revalidateLegacyService(target legacyServiceTarget) (bool, error) {
	current, _, err := inspectLegacyProcess(target.state.PID)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current != target.identity {
		return false, fmt.Errorf("旧服务进程身份漂移，拒绝继续发送信号")
	}
	return true, nil
}

func stopVerifiedLegacyService(ctx context.Context, target legacyServiceTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	alive, err := revalidateLegacyService(target)
	if err != nil || !alive {
		return err
	}
	if current, err := readRuntimeState(); err != nil || current != target.state {
		return fmt.Errorf("旧服务运行记录已变化，拒绝停止")
	}
	if target.systemd {
		if err := stopLegacySystemdService(ctx, target); err != nil {
			return err
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := ctx.Err(); err != nil {
			return err
		}
		alive, err := revalidateLegacyService(target)
		if err != nil || !alive {
			return err
		}
		// 只向已验证的服务 PID 发信号，不扩大到可能包含 shell 的进程组。
		if err := signalPID(target.state.PID, sig); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		deadline := time.NewTimer(10 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		finished := false
		for !finished {
			select {
			case <-ctx.Done():
				ticker.Stop()
				deadline.Stop()
				return ctx.Err()
			case <-deadline.C:
				finished = true
			case <-ticker.C:
				alive, err := revalidateLegacyService(target)
				if err != nil || !alive {
					ticker.Stop()
					deadline.Stop()
					return err
				}
			}
		}
		ticker.Stop()
	}
	return fmt.Errorf("旧服务 PID %d 未退出，尚未停止 Codex Host", target.state.PID)
}
