package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	codexHostConflictDisplayLimit = 4
	codexHostConflictPIDLimit     = 4
	codexHostSnapshotScanLimit    = 4 << 20
)

var ErrCodexHostConflict = errors.New("检测到未获授权的 Codex Host")

type codexHostProcessSnapshot struct {
	PID        int
	PPID       int
	PGID       int
	UID        uint32
	Executable string
	Command    string
	Args       []string
}

type codexHostProcessGroup struct {
	PGID int
	UID  uint32
	PIDs []int
	Kind string
}

func codexHostConflictUIDSet(currentUID, targetUID uint32, isolated bool) map[uint32]struct{} {
	allowed := map[uint32]struct{}{currentUID: {}}
	if isolated {
		// sudo keeps a root wrapper waiting in the same process group even when
		// the app-server child has already switched to the configured target UID.
		allowed[0] = struct{}{}
		allowed[targetUID] = struct{}{}
	}
	return allowed
}

func codexHostProcessArgsGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func (a *ACPAgent) codexHostConflictAllowedUIDs() (map[uint32]struct{}, error) {
	currentUID := uint32(os.Geteuid())
	if !a.runAs.shouldIsolate() {
		return codexHostConflictUIDSet(currentUID, 0, false), nil
	}
	target, err := user.Lookup(strings.TrimSpace(a.runAs.User))
	if err != nil {
		return nil, fmt.Errorf("解析 run_as_user %q: %w", a.runAs.User, err)
	}
	targetUID, err := strconv.ParseUint(target.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("解析 run_as_user %q 的 UID: %w", a.runAs.User, err)
	}
	return codexHostConflictUIDSet(currentUID, uint32(targetUID), true), nil
}

func parseCodexHostProcessSnapshot(output string) ([]codexHostProcessSnapshot, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64<<10), codexHostSnapshotScanLimit)
	processes := make([]codexHostProcessSnapshot, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			return nil, fmt.Errorf("解析 Codex Host 进程表第 %d 行: 字段不足", lineNumber)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("解析 Codex Host 进程表第 %d 行 PID", lineNumber)
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			return nil, fmt.Errorf("解析 Codex Host 进程表第 %d 行 PPID", lineNumber)
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil || pgid <= 0 {
			return nil, fmt.Errorf("解析 Codex Host 进程表第 %d 行 PGID", lineNumber)
		}
		uid, err := strconv.ParseUint(fields[3], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("解析 Codex Host 进程表第 %d 行 UID", lineNumber)
		}
		processes = append(processes, codexHostProcessSnapshot{
			PID: pid, PPID: ppid, PGID: pgid, UID: uint32(uid),
			Executable: fields[4], Command: strings.Join(fields[5:], " "),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("解析 Codex Host 进程表: %w", err)
	}
	if len(processes) == 0 {
		return nil, fmt.Errorf("解析 Codex Host 进程表: 输出为空")
	}
	return processes, nil
}

func collectCodexHostProcessGroups(
	processes []codexHostProcessSnapshot,
	allowedUIDs map[uint32]struct{},
) []codexHostProcessGroup {
	byPGID := make(map[int]*codexHostProcessGroup)
	for _, process := range processes {
		if _, allowed := allowedUIDs[process.UID]; !allowed ||
			!codexAppServerHostProcess(process.Executable, process.Command, process.Args) {
			continue
		}
		group := byPGID[process.PGID]
		if group == nil {
			group = &codexHostProcessGroup{
				PGID: process.PGID,
				UID:  process.UID,
				Kind: codexHostProcessKind(process.Command),
			}
			byPGID[process.PGID] = group
		}
		group.PIDs = append(group.PIDs, process.PID)
	}
	groups := make([]codexHostProcessGroup, 0, len(byPGID))
	for _, group := range byPGID {
		sort.Ints(group.PIDs)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].PGID < groups[j].PGID })
	return groups
}

func codexAppServerHostProcess(executable, command string, exactArgs ...[]string) bool {
	var (
		args []string
		ok   bool
	)
	if len(exactArgs) > 0 && exactArgs[0] != nil {
		args, ok = codexHostExactCommandArgs(executable, exactArgs[0])
	} else {
		args, ok = codexHostCommandArgs(executable, command)
	}
	if !ok {
		return false
	}
	for index := 0; index < len(args); index++ {
		field := strings.Trim(args[index], "\"'")
		if field == "--remote" || strings.HasPrefix(field, "--remote=") {
			return false
		}
		if field == "--" {
			return false
		} else if strings.HasPrefix(field, "-") {
			if codexImageOption(field) {
				if !strings.Contains(field, "=") {
					index++
				}
				for index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
					index++
				}
				continue
			}
			if codexGlobalOptionConsumesValue(field) && !strings.Contains(field, "=") {
				index++
			}
			continue
		}
		if field != "app-server" {
			return false
		}
		return codexAppServerArgsRunHost(args[index+1:])
	}
	return false
}

func codexAppServerArgsRunHost(args []string) bool {
	unknownOption := false
	for index := 0; index < len(args); index++ {
		field := strings.Trim(args[index], "\"'")
		if field == "-h" || field == "--help" {
			return false
		}
		if field == "--" {
			return true
		}
		if strings.HasPrefix(field, "-") {
			if codexAppServerOptionConsumesValue(field) && !strings.Contains(field, "=") {
				index++
				continue
			}
			if !codexAppServerKnownFlag(field) && !codexAppServerOptionConsumesValue(field) {
				unknownOption = true
			}
			continue
		}
		switch field {
		case "daemon", "proxy", "generate-ts", "generate-json-schema", "help":
			if unknownOption {
				return true
			}
			return false
		default:
			// Unknown values may belong to a newer value-taking Host option. The
			// safety gate must treat ambiguity as a possible Host, not fail open.
			return true
		}
	}
	return true
}

func codexAppServerOptionConsumesValue(option string) bool {
	if separator := strings.IndexByte(option, '='); separator >= 0 {
		option = option[:separator]
	}
	switch option {
	case "-c", "--config", "--enable", "--disable", "--code-mode-host", "--listen",
		"--ws-auth", "--ws-token-file", "--ws-token-sha256", "--ws-shared-secret-file",
		"--ws-issuer", "--ws-audience", "--ws-max-clock-skew-seconds":
		return true
	default:
		return false
	}
}

func codexAppServerKnownFlag(option string) bool {
	if separator := strings.IndexByte(option, '='); separator >= 0 {
		option = option[:separator]
	}
	switch option {
	case "--strict-config", "--stdio", "--analytics-default-enabled":
		return true
	default:
		return false
	}
}

func codexHostExactCommandArgs(executable string, argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	launcher := strings.ToLower(filepath.Base(strings.Trim(executable, "\"'")))
	if launcher == "" {
		launcher = strings.ToLower(filepath.Base(strings.Trim(argv[0], "\"'")))
	}
	switch launcher {
	case "codex", "codex.exe":
		return argv[1:], true
	case "node", "node.exe":
		if len(argv) < 2 {
			return nil, false
		}
		name := strings.ToLower(filepath.Base(strings.Trim(argv[1], "\"'")))
		if name == "codex" || name == "codex.js" {
			return argv[2:], true
		}
	}
	return nil, false
}

func codexImageOption(option string) bool {
	return option == "-i" || option == "--image" ||
		strings.HasPrefix(option, "-i=") || strings.HasPrefix(option, "--image=")
}

func codexHostCommandArgs(executable, command string) ([]string, bool) {
	explicitLauncher := strings.TrimSpace(executable) != ""
	launcher := strings.ToLower(filepath.Base(strings.Trim(executable, "\"'")))
	if launcher == "" {
		fields := strings.Fields(command)
		if len(fields) > 0 {
			launcher = strings.ToLower(filepath.Base(strings.Trim(fields[0], "\"'")))
		}
	}
	var names []string
	switch launcher {
	case "codex", "codex.exe":
		names = []string{"codex.exe", "codex"}
	case "node", "node.exe":
		names = []string{"codex.js", "codex"}
	default:
		if explicitLauncher {
			return nil, false
		}
		// The executable path in `ps command=` is not quoted. An absolute path
		// containing spaces therefore needs a conservative basename fallback.
		names = []string{"codex.exe", "codex"}
	}
	tail, ok := commandTailAfterExecutable(command, names)
	if !ok {
		return nil, false
	}
	return strings.Fields(tail), true
}

func commandTailAfterExecutable(command string, names []string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	lower := strings.ToLower(trimmed)
	bestEnd := -1
	for _, name := range names {
		name = strings.ToLower(name)
		for offset := 0; offset < len(lower); {
			index := strings.Index(lower[offset:], name)
			if index < 0 {
				break
			}
			index += offset
			end := index + len(name)
			beforeOK := index == 0 || lower[index-1] == '/' || lower[index-1] == '\\'
			afterOK := end == len(lower) || lower[end] == ' ' || lower[end] == '\t'
			if beforeOK && afterOK && (bestEnd < 0 || end < bestEnd) {
				bestEnd = end
			}
			offset = index + 1
		}
	}
	if bestEnd < 0 {
		return "", false
	}
	return strings.TrimSpace(trimmed[bestEnd:]), true
}

func codexGlobalOptionConsumesValue(option string) bool {
	switch option {
	case "-a", "--ask-for-approval",
		"-c", "--config",
		"-C", "--cd",
		"-m", "--model",
		"-p", "--profile",
		"-s", "--sandbox",
		"--add-dir", "--disable", "--enable", "--local-provider",
		"--remote-auth-token-env":
		return true
	default:
		return false
	}
}

func codexHostProcessKind(command string) string {
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "/chatgpt.app/") || strings.Contains(lower, "/codex.app/"):
		return "Codex App 私有 Host"
	case strings.Contains(lower, "/packages/standalone/"):
		return "Codex official daemon"
	case strings.HasPrefix(strings.ToLower(filepath.Base(strings.Fields(command)[0])), "node"):
		return "Codex Node app-server"
	default:
		return "Codex app-server"
	}
}

func validateCodexHostProcessGroups(groups []codexHostProcessGroup, allowedPGIDs map[int]struct{}) error {
	conflicts := make([]codexHostProcessGroup, 0, len(groups))
	for _, group := range groups {
		if _, allowed := allowedPGIDs[group.PGID]; !allowed {
			conflicts = append(conflicts, group)
		}
	}
	if len(conflicts) == 0 {
		return nil
	}

	displayed := conflicts
	if len(displayed) > codexHostConflictDisplayLimit {
		displayed = displayed[:codexHostConflictDisplayLimit]
	}
	items := make([]string, 0, len(displayed)+1)
	for _, conflict := range displayed {
		items = append(items, fmt.Sprintf("%s（PGID %d，PID %s）", conflict.Kind, conflict.PGID, boundedCodexHostPIDs(conflict.PIDs)))
	}
	if hidden := len(conflicts) - len(displayed); hidden > 0 {
		items = append(items, fmt.Sprintf("另有 %d 个进程组", hidden))
	}
	return fmt.Errorf(
		"%w：受检 UID 范围内发现 %d 个额外 app-server 进程组：%s。为避免多个 Host 并发写入，本次操作已失败关闭；只读预检未停止任何进程，WeClaw 不会停止既有或未知进程。请完整退出不再使用的 Codex App，或手动确认并结束列出的 Host 进程组后重试",
		ErrCodexHostConflict,
		len(conflicts),
		strings.Join(items, "；"),
	)
}

func boundedCodexHostPIDs(pids []int) string {
	displayed := pids
	if len(displayed) > codexHostConflictPIDLimit {
		displayed = displayed[:codexHostConflictPIDLimit]
	}
	parts := make([]string, 0, len(displayed)+1)
	for _, pid := range displayed {
		parts = append(parts, strconv.Itoa(pid))
	}
	if hidden := len(pids) - len(displayed); hidden > 0 {
		parts = append(parts, fmt.Sprintf("另有%d个", hidden))
	}
	return strings.Join(parts, ",")
}

func (a *ACPAgent) preflightCodexHostConflicts(ctx context.Context, authorityPID int) error {
	if a.codexHostConflictPreflightCall != nil {
		return a.codexHostConflictPreflightCall(ctx, authorityPID)
	}
	snapshot := a.codexHostProcessSnapshotCall
	if snapshot == nil {
		snapshot = systemCodexHostProcessSnapshot
	}
	allowedUIDs, err := a.codexHostConflictAllowedUIDs()
	if err != nil {
		return fmt.Errorf(
			"解析 Codex Host 预检身份失败；本次操作已失败关闭，只读预检未停止任何进程: %w",
			err,
		)
	}
	processes, err := snapshot(ctx, allowedUIDs)
	if err != nil {
		return fmt.Errorf(
			"读取 Codex Host 进程表失败；为避免误启第二个 Host，本次操作已失败关闭，只读预检未停止任何进程: %w",
			err,
		)
	}
	groups := collectCodexHostProcessGroups(processes, allowedUIDs)
	allowedPGIDs := make(map[int]struct{}, 1)
	if authorityPID > 0 {
		authorityPGID := 0
		for _, process := range processes {
			if process.PID != authorityPID {
				continue
			}
			if _, allowed := allowedUIDs[process.UID]; !allowed {
				return fmt.Errorf(
					"Codex Host 权威 PID %d 的进程身份无法通过只读预检；本次操作已失败关闭，只读预检未停止任何进程",
					authorityPID,
				)
			}
			authorityPGID = process.PGID
			break
		}
		if authorityPGID == 0 {
			return fmt.Errorf(
				"Codex Host 权威 PID %d 不在当前进程快照中；本次操作已失败关闭，只读预检未停止任何进程",
				authorityPID,
			)
		}
		for _, group := range groups {
			if group.PGID == authorityPGID {
				allowedPGIDs[authorityPGID] = struct{}{}
				break
			}
		}
		if _, found := allowedPGIDs[authorityPGID]; !found {
			return fmt.Errorf(
				"Codex Host 权威 PID %d 对应的进程组不包含可验证 app-server；本次操作已失败关闭，只读预检未停止任何进程",
				authorityPID,
			)
		}
	}
	return validateCodexHostProcessGroups(groups, allowedPGIDs)
}

func (a *ACPAgent) preflightConnectedManagedCodexHost(ctx context.Context, socketPath string) error {
	if a.codexHostConflictPreflightCall != nil {
		return a.codexHostConflictPreflightCall(ctx, 0)
	}
	if _, err := os.Lstat(codexHostMetadataPath(socketPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("已连接的 Codex Host 缺少受保护 metadata，权威身份无法确认；本次操作已失败关闭，只读预检未停止任何进程")
		}
		return fmt.Errorf("检查已连接的 Codex Host 元数据: %w", err)
	}
	metadata, err := a.validateManagedCodexHost(socketPath)
	if err != nil {
		return fmt.Errorf("验证已连接的 Codex Host: %w", err)
	}
	return a.preflightCodexHostConflicts(ctx, metadata.PID)
}
