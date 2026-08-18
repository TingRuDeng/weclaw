package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

const (
	codexHostModeAuto    = "auto"
	codexHostModeDaemon  = "daemon"
	codexHostModeManaged = "managed"

	codexHostManagerWeClaw = "weclaw"
	codexHostManagerDaemon = "codex_daemon"

	// 官方 daemon 的 start 子命令只等待约十秒；首次模型目录刷新较慢时，
	// 受管进程可能仍在继续启动。额外等待仍必须通过 version 和进程元数据复核。
	codexDaemonLateReadyMultiplier = 6
)

var (
	errCodexDaemonInstallRequired = errors.New("Codex 官方 daemon 需要 standalone 安装")
	errCodexDaemonUnmanaged       = errors.New("Codex control socket 不是官方 daemon 受管 Host")
)

type codexDaemonLifecycleOutput struct {
	Status              string `json:"status"`
	Backend             string `json:"backend,omitempty"`
	PID                 int    `json:"pid,omitempty"`
	ManagedCodexPath    string `json:"managedCodexPath"`
	ManagedCodexVersion string `json:"managedCodexVersion,omitempty"`
	SocketPath          string `json:"socketPath"`
	CLIVersion          string `json:"cliVersion,omitempty"`
	AppServerVersion    string `json:"appServerVersion,omitempty"`
}

type codexDaemonPIDRecord struct {
	PID              int    `json:"pid"`
	ProcessStartTime string `json:"processStartTime"`
}

func normalizeAgentCodexHostMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	mode = strings.ReplaceAll(mode, "-", "_")
	switch mode {
	case codexHostModeAuto, codexHostModeDaemon, codexHostModeManaged:
		return mode
	case "":
		// Direct agent constructors predate host-mode configuration and are
		// heavily used by embedders/tests. The config layer passes explicit
		// "auto" for normal WeClaw startup.
		return codexHostModeManaged
	default:
		return codexHostModeManaged
	}
}

// resolveAgentCodexHostMode freezes auto mode at construction time. A custom
// socket or run_as_user keeps the compatibility backend. Otherwise an existing
// official socket or standalone install selects daemon; daemon failures never
// fall back to a second Host in the same process.
func (a *ACPAgent) resolveAgentCodexHostMode() string {
	switch a.codexHostMode {
	case codexHostModeDaemon, codexHostModeManaged:
		return a.codexHostMode
	}
	if strings.TrimSpace(a.codexHostSocketSnapshot()) != "" || a.runAs.shouldIsolate() {
		return codexHostModeManaged
	}
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return codexHostModeManaged
	}
	socketPath := codexDaemonSocketPath(codexHome)
	if info, statErr := os.Lstat(socketPath); statErr == nil &&
		info.Mode()&os.ModeSymlink == 0 && info.Mode()&os.ModeSocket != 0 {
		return codexHostModeDaemon
	}
	if codexDaemonStandaloneAvailable(codexHome) {
		return codexHostModeDaemon
	}
	return codexHostModeManaged
}

func (a *ACPAgent) usesOfficialCodexDaemon() bool {
	return a.codexHostMode == codexHostModeDaemon
}

func codexDaemonSocketPath(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

func codexDaemonManagedBinaryPath(codexHome string) string {
	return filepath.Join(codexHome, "packages", "standalone", "current", "codex")
}

func codexDaemonPIDPath(codexHome string) string {
	return filepath.Join(codexHome, "app-server-daemon", "app-server.pid")
}

func codexDaemonStandaloneAvailable(codexHome string) bool {
	info, err := os.Stat(codexDaemonManagedBinaryPath(codexHome))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func (a *ACPAgent) resolveCodexDaemonLifecycleCommand() (string, error) {
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return "", fmt.Errorf("resolve CODEX_HOME for official daemon: %w", err)
	}
	command := codexDaemonManagedBinaryPath(codexHome)
	if !codexDaemonStandaloneAvailable(codexHome) {
		return "", fmt.Errorf(
			"%w: expected executable %s",
			errCodexDaemonInstallRequired,
			command,
		)
	}
	return command, nil
}

func (a *ACPAgent) resolveCodexDaemonSocket() (string, error) {
	if a.runAs.shouldIsolate() {
		return "", fmt.Errorf("official Codex daemon does not support run_as_user")
	}
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return "", fmt.Errorf("resolve CODEX_HOME for official daemon: %w", err)
	}
	socketPath := filepath.Clean(codexDaemonSocketPath(codexHome))
	if configured := strings.TrimSpace(a.codexHostSocketSnapshot()); configured != "" &&
		filepath.Clean(configured) != socketPath {
		return "", fmt.Errorf("official Codex daemon does not accept app_server_socket overrides")
	}
	if len([]byte(socketPath)) > codexHostSocketMaxBytes {
		return "", fmt.Errorf(
			"official Codex daemon socket path is too long (%d bytes, max %d): %s",
			len([]byte(socketPath)),
			codexHostSocketMaxBytes,
			socketPath,
		)
	}
	a.mu.Lock()
	a.codexHostSocket = socketPath
	a.mu.Unlock()
	return socketPath, nil
}

func (a *ACPAgent) launchCodexDaemonClient(ctx context.Context, socketPath string) (int, error) {
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return 0, err
	}
	lifecycleLock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return 0, err
	}
	defer releaseCodexHostStartupLock(lifecycleLock)
	return a.launchCodexDaemonClientLocked(ctx, socketPath)
}

// launchCodexDaemonClientLocked attaches only after the official lifecycle
// command proves backend=pid. A responsive but unmanaged socket is rejected
// instead of being adopted or replaced.
func (a *ACPAgent) launchCodexDaemonClientLocked(ctx context.Context, socketPath string) (int, error) {
	if err := a.prepareCodexHostSocket(socketPath); err != nil {
		return 0, err
	}
	if conn, dialErr := dialCodexHost(ctx, socketPath); dialErr == nil {
		output, inspectErr := a.runAndValidateCodexDaemonLifecycle(ctx, "version", socketPath)
		if inspectErr != nil {
			_ = conn.Close()
			return 0, inspectErr
		}
		if err := a.preflightCodexHostConflicts(ctx, output.PID); err != nil {
			_ = conn.Close()
			return 0, err
		}
		metadata, metadataErr := a.recordCodexDaemonMetadata(ctx, output, socketPath)
		if metadataErr != nil {
			_ = conn.Close()
			return 0, metadataErr
		}
		if err := a.ensureCodexAppReusesDaemon(ctx, socketPath); err != nil {
			_ = conn.Close()
			return 0, err
		}
		if err := a.attachCodexHostConnection(conn); err != nil {
			_ = conn.Close()
			return 0, err
		}
		return metadata.PID, nil
	}

	if err := a.preflightCodexHostConflicts(ctx, 0); err != nil {
		return 0, err
	}
	output, err := a.runAndValidateCodexDaemonLifecycle(ctx, "start", socketPath)
	if err != nil {
		if !isCodexDaemonStartReadinessTimeout(err, socketPath) {
			return 0, err
		}
		conn, waitErr := waitForCodexHost(
			ctx,
			socketPath,
			0,
			nil,
			time.Duration(codexDaemonLateReadyMultiplier)*a.effectiveCodexHostConnectTimeout(),
		)
		if waitErr != nil {
			return 0, fmt.Errorf("%v; wait for managed Codex daemon after startup timeout: %w", err, waitErr)
		}
		output, err = a.runAndValidateCodexDaemonLifecycle(ctx, "version", socketPath)
		if err != nil {
			_ = conn.Close()
			return 0, err
		}
		if err := a.preflightCodexHostConflicts(ctx, output.PID); err != nil {
			_ = conn.Close()
			return 0, err
		}
		metadata, metadataErr := a.recordCodexDaemonMetadata(ctx, output, socketPath)
		if metadataErr != nil {
			_ = conn.Close()
			return 0, metadataErr
		}
		if err := a.ensureCodexAppReusesDaemon(ctx, socketPath); err != nil {
			_ = conn.Close()
			return 0, err
		}
		if err := a.attachCodexHostConnection(conn); err != nil {
			_ = conn.Close()
			return 0, err
		}
		return metadata.PID, nil
	}
	if err := a.preflightCodexHostConflicts(ctx, output.PID); err != nil {
		return 0, err
	}
	metadata, err := a.recordCodexDaemonMetadata(ctx, output, socketPath)
	if err != nil {
		return 0, err
	}
	conn, err := waitForCodexHost(
		ctx,
		socketPath,
		metadata.PID,
		nil,
		a.effectiveCodexHostConnectTimeout(),
	)
	if err != nil {
		return 0, err
	}
	if err := a.ensureCodexAppReusesDaemon(ctx, socketPath); err != nil {
		_ = conn.Close()
		return 0, err
	}
	if err := a.attachCodexHostConnection(conn); err != nil {
		_ = conn.Close()
		return 0, err
	}
	return metadata.PID, nil
}

func isCodexDaemonStartReadinessTimeout(err error, socketPath string) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "app server did not become ready on") &&
		strings.Contains(text, strings.ToLower(filepath.Clean(socketPath)))
}

func (a *ACPAgent) runAndValidateCodexDaemonLifecycle(
	ctx context.Context,
	action string,
	socketPath string,
) (codexDaemonLifecycleOutput, error) {
	output, err := a.runCodexDaemonLifecycle(ctx, action)
	if err != nil {
		return codexDaemonLifecycleOutput{}, classifyCodexDaemonCommandError(err)
	}
	if filepath.Clean(output.SocketPath) != filepath.Clean(socketPath) {
		return codexDaemonLifecycleOutput{}, fmt.Errorf(
			"%w: lifecycle socket=%s, expected=%s",
			errCodexDaemonUnmanaged,
			output.SocketPath,
			socketPath,
		)
	}
	if action == "version" && (output.Backend == "" || output.PID <= 0) {
		output, err = a.hydrateCodexDaemonVersionOutput(output, socketPath)
		if err != nil {
			return codexDaemonLifecycleOutput{}, err
		}
	}
	switch action {
	case "start":
		if output.Status != "started" && output.Status != "alreadyRunning" {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("unexpected Codex daemon start status %q", output.Status)
		}
	case "version":
		if output.Status != "running" {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("unexpected Codex daemon version status %q", output.Status)
		}
	case "stop":
		if output.Status != "stopped" && output.Status != "notRunning" {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("unexpected Codex daemon stop status %q", output.Status)
		}
	}
	if action != "stop" && output.Backend != "pid" {
		return codexDaemonLifecycleOutput{}, fmt.Errorf("%w: backend=%q", errCodexDaemonUnmanaged, output.Backend)
	}
	if action != "stop" && strings.TrimSpace(output.ManagedCodexPath) == "" {
		return codexDaemonLifecycleOutput{}, fmt.Errorf("%w: managed Codex path is empty", errCodexDaemonUnmanaged)
	}
	if action != "stop" && output.PID <= 0 {
		return codexDaemonLifecycleOutput{}, fmt.Errorf("%w: lifecycle PID is invalid", errCodexDaemonUnmanaged)
	}
	return output, nil
}

// hydrateCodexDaemonVersionOutput handles official standalone releases whose
// version response reports the running socket and versions but omits the
// lifecycle backend and PID. The identity comes from the official lifecycle
// PID record when available, or from an existing protected WeClaw daemon
// metadata record when the release does not create the legacy PID file.
func (a *ACPAgent) hydrateCodexDaemonVersionOutput(
	output codexDaemonLifecycleOutput,
	socketPath string,
) (codexDaemonLifecycleOutput, error) {
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return codexDaemonLifecycleOutput{}, fmt.Errorf(
			"%w: resolve CODEX_HOME for official daemon PID record: %v",
			errCodexDaemonUnmanaged,
			err,
		)
	}
	record, err := a.readCodexDaemonPIDRecordForSocket(codexHome, socketPath)
	if err != nil {
		return codexDaemonLifecycleOutput{}, fmt.Errorf(
			"%w: read official Codex daemon identity: %v",
			errCodexDaemonUnmanaged,
			err,
		)
	}
	if output.PID > 0 && output.PID != record.PID {
		return codexDaemonLifecycleOutput{}, fmt.Errorf(
			"%w: lifecycle pid=%d, record pid=%d",
			errCodexDaemonUnmanaged,
			output.PID,
			record.PID,
		)
	}
	if output.Backend == "" {
		output.Backend = "pid"
	}
	if output.PID <= 0 {
		output.PID = record.PID
	}
	return output, nil
}

func classifyCodexDaemonCommandError(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "managed standalone codex install not found"):
		return fmt.Errorf("%w: %v", errCodexDaemonInstallRequired, err)
	case strings.Contains(text, "app server is running but is not managed by codex app-server daemon"):
		return fmt.Errorf("%w: %v", errCodexDaemonUnmanaged, err)
	case IsCodexStateRuntimeError(err):
		return newCodexStateRuntimeFailure(classifyCodexStateRuntimeFailure(err), err)
	default:
		return err
	}
}

func (a *ACPAgent) runCodexDaemonLifecycle(
	ctx context.Context,
	action string,
) (codexDaemonLifecycleOutput, error) {
	if a.codexDaemonLifecycleCall != nil {
		return a.codexDaemonLifecycleCall(ctx, action)
	}
	command, err := a.resolveCodexDaemonLifecycleCommand()
	if err != nil {
		return codexDaemonLifecycleOutput{}, err
	}
	args := codexDaemonCommandArgs(a.args, action)
	command, args = a.runAs.wrapCommand(command, args)
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = a.cwd
	configureACPProcess(cmd)
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("build Codex daemon environment: %w", err)
		}
		cmd.Env = cmdEnv
	}
	stdout := &boundedCommandOutput{limit: codexCLICommandOutputLimit}
	stderr := &boundedCommandOutput{limit: codexCLICommandOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("codex app-server daemon %s: %w", action, err)
		}
		return codexDaemonLifecycleOutput{}, fmt.Errorf(
			"codex app-server daemon %s: %w: %s",
			action,
			err,
			detail,
		)
	}
	return parseCodexDaemonLifecycleOutput(stdout.String())
}

func codexDaemonCommandArgs(configured []string, action string) []string {
	prefix := make([]string, 0, len(configured)+3)
	for _, arg := range configured {
		if arg == "app-server" {
			break
		}
		prefix = append(prefix, arg)
	}
	return append(prefix, "app-server", "daemon", action)
}

func parseCodexDaemonLifecycleOutput(data string) (codexDaemonLifecycleOutput, error) {
	decoder := json.NewDecoder(strings.NewReader(data))
	var output codexDaemonLifecycleOutput
	if err := decoder.Decode(&output); err != nil {
		return codexDaemonLifecycleOutput{}, fmt.Errorf("parse Codex daemon lifecycle output: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return codexDaemonLifecycleOutput{}, fmt.Errorf("parse Codex daemon lifecycle output: multiple JSON values")
		}
		return codexDaemonLifecycleOutput{}, fmt.Errorf("parse Codex daemon lifecycle output trailer: %w", err)
	}
	return output, nil
}

func (a *ACPAgent) recordCodexDaemonMetadata(
	ctx context.Context,
	output codexDaemonLifecycleOutput,
	socketPath string,
) (codexHostMetadata, error) {
	if a.codexDaemonMetadataCall != nil {
		metadata, err := a.codexDaemonMetadataCall(ctx, output, socketPath)
		if err != nil {
			return codexHostMetadata{}, err
		}
		if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
			return codexHostMetadata{}, err
		}
		return metadata, nil
	}
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return codexHostMetadata{}, err
	}
	expectedManagedPath := filepath.Clean(codexDaemonManagedBinaryPath(codexHome))
	if filepath.Clean(output.ManagedCodexPath) != expectedManagedPath {
		return codexHostMetadata{}, fmt.Errorf(
			"%w: managed Codex path=%s, expected=%s",
			errCodexDaemonUnmanaged,
			output.ManagedCodexPath,
			expectedManagedPath,
		)
	}
	record, err := a.readCodexDaemonPIDRecordForSocket(codexHome, socketPath)
	if err != nil {
		return codexHostMetadata{}, err
	}
	if output.PID > 0 && output.PID != record.PID {
		return codexHostMetadata{}, fmt.Errorf(
			"%w: lifecycle pid=%d, record pid=%d",
			errCodexDaemonUnmanaged,
			output.PID,
			record.PID,
		)
	}
	identity, err := inspectCodexHostProcess(record.PID)
	if err != nil {
		return codexHostMetadata{}, fmt.Errorf("inspect official Codex daemon process: %w", err)
	}
	if identity.pgid != record.PID {
		return codexHostMetadata{}, fmt.Errorf("%w: daemon process is not in its own session", errCodexDaemonUnmanaged)
	}
	if identity.start != record.ProcessStartTime {
		return codexHostMetadata{}, fmt.Errorf("%w: daemon process start time mismatch", errCodexDaemonUnmanaged)
	}
	command, err := psProcessField(record.PID, "command=")
	if err != nil {
		return codexHostMetadata{}, err
	}
	if !codexDaemonProcessCommandMatches(command, expectedManagedPath) {
		return codexHostMetadata{}, fmt.Errorf("%w: managed process command mismatch", errCodexDaemonUnmanaged)
	}

	generation := uint64(1)
	var activeProfileID string
	var accountFingerprint string
	startedAt := time.Now().UTC()
	if previous, readErr := a.readCodexHostMetadata(socketPath); readErr == nil {
		sameProcess := previous.Manager == codexHostManagerDaemon &&
			previous.PID == record.PID &&
			previous.UID == identity.uid &&
			previous.ProcessStart == identity.start &&
			previous.ObservedCommandHash == identity.commandHash
		if sameProcess {
			generation = previous.Generation
			activeProfileID = previous.ActiveProfileID
			accountFingerprint = previous.AccountFingerprint
			startedAt = previous.StartedAt
		} else if previous.Generation > 0 {
			generation = previous.Generation + 1
		}
	}
	metadata := codexHostMetadata{
		Version:             codexHostMetadataVersion,
		Manager:             codexHostManagerDaemon,
		State:               "running",
		PID:                 record.PID,
		ProcessGroupID:      identity.pgid,
		UID:                 identity.uid,
		ProcessStart:        identity.start,
		ObservedCommandHash: identity.commandHash,
		CommandFingerprint:  a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath:          socketPath,
		Generation:          generation,
		ActiveProfileID:     activeProfileID,
		AccountFingerprint:  accountFingerprint,
		ManagedCodexPath:    output.ManagedCodexPath,
		AppServerVersion:    output.AppServerVersion,
		StartedAt:           startedAt,
	}
	if err := a.writeManagedCodexHostMetadata(socketPath, metadata); err != nil {
		return codexHostMetadata{}, err
	}
	return metadata, nil
}

func codexDaemonProcessCommandMatches(command, managedCodexPath string) bool {
	command = strings.TrimSpace(command)
	managedCodexPath = filepath.Clean(strings.TrimSpace(managedCodexPath))
	if command == "" || managedCodexPath == "." || !strings.HasPrefix(command, managedCodexPath) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(command, managedCodexPath))
	return rest == "app-server" || strings.HasPrefix(rest, "app-server ")
}

func (a *ACPAgent) validateCodexDaemonManagement(
	ctx context.Context,
	socketPath string,
	metadata codexHostMetadata,
) error {
	output, err := a.runAndValidateCodexDaemonLifecycle(ctx, "version", socketPath)
	if err != nil {
		return err
	}
	if filepath.Clean(output.ManagedCodexPath) != filepath.Clean(metadata.ManagedCodexPath) {
		return fmt.Errorf("%w: managed Codex path changed", errCodexDaemonUnmanaged)
	}
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return err
	}
	record, err := a.readCodexDaemonPIDRecordForSocket(codexHome, socketPath)
	if err != nil {
		return err
	}
	if record.PID != metadata.PID || record.ProcessStartTime != metadata.ProcessStart {
		return codexauth.NewError(
			codexauth.CodeConflict,
			"Codex daemon 已切换到新一代，请重试账号操作",
			nil,
		)
	}
	return nil
}

func (a *ACPAgent) verifyCodexDaemonStopped(
	ctx context.Context,
	socketPath string,
	expected codexHostMetadata,
) error {
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return err
	}
	record, recordErr := a.readCodexDaemonPIDRecord(codexDaemonPIDPath(codexHome))
	switch {
	case recordErr == nil:
		if record.PID == expected.PID && record.ProcessStartTime == expected.ProcessStart {
			return codexauth.NewError(
				codexauth.CodeConflict,
				"Codex daemon stop 返回后原 Host 仍有活动记录",
				nil,
			)
		}
		return codexauth.NewError(
			codexauth.CodeConflict,
			"Codex daemon 在停止过程中已切换到新一代",
			nil,
		)
	case !errors.Is(recordErr, os.ErrNotExist):
		return recordErr
	}
	probeCtx, cancel := context.WithTimeout(ctx, codexHostDialTimeout)
	defer cancel()
	if conn, dialErr := dialCodexHost(probeCtx, socketPath); dialErr == nil {
		_ = conn.Close()
		return codexauth.NewError(
			codexauth.CodeConflict,
			"Codex daemon stop 返回后 control socket 仍可写",
			nil,
		)
	}
	return nil
}

func (a *ACPAgent) readCodexDaemonPIDRecord(path string) (codexDaemonPIDRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return codexDaemonPIDRecord{}, fmt.Errorf("read official Codex daemon pid record: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return codexDaemonPIDRecord{}, fmt.Errorf("%w: daemon pid record must be a regular file", errCodexDaemonUnmanaged)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return codexDaemonPIDRecord{}, fmt.Errorf("%w: daemon pid record is group/world writable", errCodexDaemonUnmanaged)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return codexDaemonPIDRecord{}, fmt.Errorf("inspect official Codex daemon pid record owner")
	}
	if _, allowed := a.allowedCodexHostUIDs()[stat.Uid]; !allowed {
		return codexDaemonPIDRecord{}, fmt.Errorf("%w: daemon pid record owner uid=%d", errCodexDaemonUnmanaged, stat.Uid)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return codexDaemonPIDRecord{}, err
	}
	var record codexDaemonPIDRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return codexDaemonPIDRecord{}, fmt.Errorf("parse official Codex daemon pid record: %w", err)
	}
	if record.PID <= 0 || strings.TrimSpace(record.ProcessStartTime) == "" {
		return codexDaemonPIDRecord{}, fmt.Errorf("%w: invalid daemon pid record", errCodexDaemonUnmanaged)
	}
	return record, nil
}

// readCodexDaemonPIDRecordForSocket supports standalone releases that expose
// lifecycle state through the control socket but do not create the historical
// app-server-daemon/app-server.pid file. The fallback is accepted only for a
// protected, currently-running daemon metadata record; callers still inspect
// the process start time, process group, and managed command before attaching.
func (a *ACPAgent) readCodexDaemonPIDRecordForSocket(
	codexHome string,
	socketPath string,
) (codexDaemonPIDRecord, error) {
	record, err := a.readCodexDaemonPIDRecord(codexDaemonPIDPath(codexHome))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return record, err
	}
	metadata, metadataErr := a.readCodexHostMetadata(socketPath)
	if metadataErr != nil {
		return codexDaemonPIDRecord{}, fmt.Errorf(
			"official PID record is unavailable (%v); read protected daemon metadata: %w",
			err,
			metadataErr,
		)
	}
	if metadata.Manager != codexHostManagerDaemon || metadata.State != "running" {
		return codexDaemonPIDRecord{}, fmt.Errorf(
			"%w: protected daemon metadata is not a running official daemon",
			errCodexDaemonUnmanaged,
		)
	}
	expectedManagedPath := filepath.Clean(codexDaemonManagedBinaryPath(codexHome))
	if filepath.Clean(metadata.ManagedCodexPath) != expectedManagedPath {
		return codexDaemonPIDRecord{}, fmt.Errorf(
			"%w: protected daemon metadata managed path=%s, expected=%s",
			errCodexDaemonUnmanaged,
			metadata.ManagedCodexPath,
			expectedManagedPath,
		)
	}
	if metadata.PID <= 0 || metadata.ProcessGroupID <= 0 || strings.TrimSpace(metadata.ProcessStart) == "" {
		return codexDaemonPIDRecord{}, fmt.Errorf(
			"%w: protected daemon metadata identity is incomplete",
			errCodexDaemonUnmanaged,
		)
	}
	return codexDaemonPIDRecord{
		PID:              metadata.PID,
		ProcessStartTime: metadata.ProcessStart,
	}, nil
}
