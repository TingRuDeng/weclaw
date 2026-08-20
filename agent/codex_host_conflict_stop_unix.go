//go:build !windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

var (
	inspectCodexConflictProcessForStop = inspectCodexHostProcess
	errCodexConflictWaitTimeout        = errors.New("timeout")
	errCodexConflictIdentityDrift      = errors.New("Codex Host 进程身份已变化")
)

func stopCodexConflictProcessGroup(ctx context.Context, target codexVerifiedHostConflictTarget) error {
	if target.group.PGID <= 0 || len(target.members) == 0 {
		return fmt.Errorf("候选 Codex Host 进程组身份不完整")
	}
	if err := syscall.Kill(-target.group.PGID, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("停止 Codex Host 进程组 PGID %d: %w", target.group.PGID, err)
	}
	if err := waitCodexConflictMembersExit(ctx, target.members, acpKillGrace); err == nil {
		return nil
	} else if !errors.Is(err, errCodexConflictWaitTimeout) {
		return err
	}
	alive, err := verifyCodexConflictMembersCurrent(target.members)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if err := syscall.Kill(-target.group.PGID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("强制停止 Codex Host 进程组 PGID %d: %w", target.group.PGID, err)
	}
	if err := waitCodexConflictMembersExit(ctx, target.members, acpKillGrace); err != nil {
		return fmt.Errorf("等待 Codex Host 进程组 PGID %d 退出: %w", target.group.PGID, err)
	}
	return nil
}

func waitCodexConflictMembersExit(ctx context.Context, members []codexHostProcessProof, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive := false
		for _, member := range members {
			memberAlive, err := verifyCodexConflictMemberCurrent(member)
			if err != nil {
				return err
			}
			if memberAlive {
				alive = true
			}
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errCodexConflictWaitTimeout
		case <-ticker.C:
		}
	}
}

func verifyCodexConflictMembersCurrent(members []codexHostProcessProof) (bool, error) {
	alive := false
	for _, member := range members {
		memberAlive, err := verifyCodexConflictMemberCurrent(member)
		if err != nil {
			return false, err
		}
		alive = alive || memberAlive
	}
	return alive, nil
}

func verifyCodexConflictMemberCurrent(member codexHostProcessProof) (bool, error) {
	if !codexHostProcessAlive(member.PID) {
		return false, nil
	}
	identity, err := inspectCodexConflictProcessForStop(member.PID)
	if err != nil {
		if !codexHostProcessAlive(member.PID) {
			return false, nil
		}
		return false, fmt.Errorf("复核 Codex Host PID %d: %w", member.PID, err)
	}
	if identity.uid != member.UID || identity.pgid != member.PGID ||
		identity.start != member.Start || identity.commandHash != member.CommandHash {
		return false, fmt.Errorf("%w：PID %d", errCodexConflictIdentityDrift, member.PID)
	}
	return true, nil
}
