package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

const codexHostMetadataVersion = 1

type codexHostMetadata struct {
	Version             int       `json:"version"`
	State               string    `json:"state"`
	PID                 int       `json:"pid"`
	ProcessGroupID      int       `json:"process_group_id"`
	UID                 uint32    `json:"uid"`
	ProcessStart        string    `json:"process_start"`
	ObservedCommandHash string    `json:"observed_command_hash"`
	CommandFingerprint  string    `json:"command_fingerprint"`
	SocketPath          string    `json:"socket_path"`
	Generation          uint64    `json:"generation"`
	ActiveProfileID     string    `json:"active_profile_id,omitempty"`
	AccountFingerprint  string    `json:"account_fingerprint,omitempty"`
	StartedAt           time.Time `json:"started_at"`
	StoppedAt           time.Time `json:"stopped_at,omitempty"`
}

type CodexHostStatus struct {
	Managed            bool   `json:"managed"`
	Running            bool   `json:"running"`
	PID                int    `json:"pid,omitempty"`
	Generation         uint64 `json:"generation,omitempty"`
	ActiveProfileID    string `json:"active_profile_id,omitempty"`
	AccountFingerprint string `json:"account_fingerprint,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// CodexHostSupervisor 暴露共享 app-server 的受管生命周期状态。
type CodexHostSupervisor interface {
	InspectCodexHost(context.Context) CodexHostStatus
}

func codexHostMetadataPath(socketPath string) string { return socketPath + ".pid.json" }

func (a *ACPAgent) configuredCodexHostCommandFingerprint(socketPath string) string {
	args := codexSharedHostArgs(a.args, socketPath)
	command, args := a.runAs.wrapCommand(a.command, args)
	value := command + "\x00" + strings.Join(args, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *ACPAgent) writeManagedCodexHostMetadata(cmd *exec.Cmd, socketPath string) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("codex managed host process is unavailable")
	}
	identity, err := inspectCodexHostProcess(cmd.Process.Pid)
	if err != nil {
		return fmt.Errorf("inspect started codex host: %w", err)
	}
	generation := uint64(1)
	if previous, readErr := a.readCodexHostMetadata(socketPath); readErr == nil && previous.Generation > 0 {
		generation = previous.Generation + 1
	}
	metadata := codexHostMetadata{
		Version:             codexHostMetadataVersion,
		State:               "running",
		PID:                 cmd.Process.Pid,
		ProcessGroupID:      identity.pgid,
		UID:                 identity.uid,
		ProcessStart:        identity.start,
		ObservedCommandHash: identity.commandHash,
		CommandFingerprint:  a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:          socketPath,
		Generation:          generation,
		StartedAt:           time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		return fmt.Errorf("write codex managed host metadata: %w", err)
	}
	if !codexHostProcessAlive(metadata.PID) {
		a.markCodexHostMetadataStopped(socketPath, metadata.PID)
		return fmt.Errorf("codex managed host exited while recording metadata")
	}
	return nil
}

func (a *ACPAgent) readCodexHostMetadata(socketPath string) (codexHostMetadata, error) {
	path := codexHostMetadataPath(socketPath)
	info, err := os.Lstat(path)
	if err != nil {
		return codexHostMetadata{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return codexHostMetadata{}, fmt.Errorf("codex host metadata must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return codexHostMetadata{}, fmt.Errorf("codex host metadata permissions must be 0600")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return codexHostMetadata{}, fmt.Errorf("cannot inspect codex host metadata owner")
	}
	if _, allowed := a.allowedCodexHostUIDs()[stat.Uid]; !allowed {
		return codexHostMetadata{}, fmt.Errorf("codex host metadata owner is not allowed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return codexHostMetadata{}, err
	}
	var metadata codexHostMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return codexHostMetadata{}, fmt.Errorf("parse codex host metadata: %w", err)
	}
	if metadata.Version != codexHostMetadataVersion || filepath.Clean(metadata.SocketPath) != filepath.Clean(socketPath) {
		return codexHostMetadata{}, fmt.Errorf("codex host metadata identity mismatch")
	}
	return metadata, nil
}

func (a *ACPAgent) writeCodexHostMetadata(socketPath string, metadata codexHostMetadata) error {
	path := codexHostMetadataPath(socketPath)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to replace non-regular codex host metadata")
		}
		if info.Mode().Perm() != 0o600 {
			return fmt.Errorf("codex host metadata permissions must be 0600")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot inspect codex host metadata owner")
		}
		if _, allowed := a.allowedCodexHostUIDs()[stat.Uid]; !allowed {
			return fmt.Errorf("codex host metadata owner is not allowed")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".codex-host-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}

func (a *ACPAgent) markCodexHostMetadataStopped(socketPath string, pid int) {
	metadata, err := a.readCodexHostMetadata(socketPath)
	if err != nil || metadata.PID != pid {
		return
	}
	metadata.State = "stopped"
	metadata.StoppedAt = time.Now().UTC()
	_ = a.writeCodexHostMetadata(socketPath, metadata)
}

func (a *ACPAgent) updateCodexHostAccountIdentity(socketPath string, profile codexauth.Profile) error {
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return err
	}
	metadata.ActiveProfileID = string(profile.ID)
	metadata.AccountFingerprint = profile.AccountFingerprint
	return a.writeCodexHostMetadata(socketPath, metadata)
}

func (a *ACPAgent) validateManagedCodexHost(socketPath string) (codexHostMetadata, error) {
	metadata, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "当前 Codex app-server 不是 WeClaw 受管 Host；请先重启 WeClaw", err)
	}
	if metadata.State != "running" || metadata.PID <= 0 || metadata.ProcessGroupID <= 0 {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "当前没有可安全切换的 WeClaw 受管 Codex Host", nil)
	}
	if metadata.CommandFingerprint != a.configuredCodexHostCommandFingerprint(socketPath) {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "Codex Host 启动配置已变化；请先重启 WeClaw", nil)
	}
	identity, err := inspectCodexHostProcess(metadata.PID)
	if err != nil {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "Codex Host 进程身份无法确认", err)
	}
	if identity.uid != metadata.UID || identity.pgid != metadata.ProcessGroupID ||
		identity.start != metadata.ProcessStart || identity.commandHash != metadata.ObservedCommandHash {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "Codex Host 进程身份与受管记录不一致", nil)
	}
	if _, ok := a.allowedCodexHostUIDs()[identity.uid]; !ok {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "Codex Host 进程所有者不受信任", nil)
	}
	if metadata.ProcessGroupID != metadata.PID {
		return codexHostMetadata{}, codexauth.NewError(codexauth.CodeUnmanagedHost, "Codex Host 未运行在独立进程组，拒绝切换", nil)
	}
	return metadata, nil
}

func (a *ACPAgent) InspectCodexHost(ctx context.Context) CodexHostStatus {
	_ = ctx
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return CodexHostStatus{Reason: err.Error()}
	}
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return CodexHostStatus{Reason: err.Error()}
	}
	return CodexHostStatus{
		Managed: true, Running: true, PID: metadata.PID, Generation: metadata.Generation,
		ActiveProfileID: metadata.ActiveProfileID, AccountFingerprint: metadata.AccountFingerprint,
	}
}

// stopManagedCodexHostLocked 在 lifecycle lock 内停止真实 host。未知 PID、UID、
// 启动时间、命令或进程组一律拒绝，不以 socket 存在代替进程身份。
func (a *ACPAgent) stopManagedCodexHostLocked(ctx context.Context, socketPath string) error {
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return err
	}
	connection, ownedCmd, ownedDone := a.disconnectCodexHostClient(true)
	if connection != nil {
		_ = connection.Close()
	}
	a.failAppServerActiveTurns("Codex app-server stopped for account switch")
	a.failPendingRequests("Codex app-server stopped for account switch")
	if ownedCmd != nil && ownedCmd.Process != nil && ownedCmd.Process.Pid == metadata.PID {
		stopCodexHostProcess(ownedCmd, ownedDone)
	} else {
		if err := syscall.Kill(-metadata.ProcessGroupID, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stop managed codex host: %w", err)
		}
		if err := waitCodexHostProcessExit(ctx, metadata.PID, acpKillGrace); err != nil {
			if killErr := syscall.Kill(-metadata.ProcessGroupID, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				return fmt.Errorf("kill managed codex host: %w", killErr)
			}
			if err := waitCodexHostProcessExit(ctx, metadata.PID, acpKillGrace); err != nil {
				return err
			}
		}
	}
	a.markCodexHostMetadataStopped(socketPath, metadata.PID)
	return nil
}

func (a *ACPAgent) startManagedCodexHostLocked(ctx context.Context, socketPath string) error {
	pid, err := a.launchCodexHostClientLocked(ctx, socketPath)
	if err != nil {
		return err
	}
	if _, err := a.initializeACPSubprocess(ctx, pid); err != nil {
		return a.failACPStartup(pid, err)
	}
	return nil
}

type codexProcessIdentity struct {
	uid         uint32
	pgid        int
	start       string
	commandHash string
}

func inspectCodexHostProcess(pid int) (codexProcessIdentity, error) {
	if pid <= 0 {
		return codexProcessIdentity{}, fmt.Errorf("invalid pid")
	}
	uidValue, err := psProcessField(pid, "uid=")
	if err != nil {
		return codexProcessIdentity{}, err
	}
	uid64, err := strconv.ParseUint(strings.TrimSpace(uidValue), 10, 32)
	if err != nil {
		return codexProcessIdentity{}, fmt.Errorf("parse process uid: %w", err)
	}
	start, err := psProcessField(pid, "lstart=")
	if err != nil {
		return codexProcessIdentity{}, err
	}
	command, err := psProcessField(pid, "command=")
	if err != nil {
		return codexProcessIdentity{}, err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return codexProcessIdentity{}, fmt.Errorf("read process group: %w", err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	return codexProcessIdentity{
		uid: uint32(uid64), pgid: pgid, start: strings.TrimSpace(start), commandHash: hex.EncodeToString(sum[:]),
	}, nil
}

func psProcessField(pid int, field string) (string, error) {
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", field).Output()
	if err != nil {
		return "", fmt.Errorf("inspect process %d: %w", pid, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("process %d is not running", pid)
	}
	return value, nil
}

func codexHostProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitCodexHostProcessExit(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for codexHostProcessAlive(pid) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for managed Codex Host exit: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("wait for managed Codex Host exit: timeout")
		case <-ticker.C:
		}
	}
	return nil
}
