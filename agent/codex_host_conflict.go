package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/config"
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

// codexHostConflictTargetKind is intentionally narrower than process
// classification. A target is only eligible for an explicit stop after its
// type-specific ownership proof has also been revalidated.
type codexHostConflictTargetKind string

const (
	codexHostConflictTargetUnknown        codexHostConflictTargetKind = "unknown"
	codexHostConflictTargetOfficialDaemon codexHostConflictTargetKind = "official_daemon"
	codexHostConflictTargetAppPrivate     codexHostConflictTargetKind = "app_private"
	codexHostConflictTargetManaged        codexHostConflictTargetKind = "weclaw_managed"
)

// codexHostConflictTarget is a non-secret structural classification captured
// from one process-table snapshot. It never itself authorizes a signal.
type codexHostConflictTarget struct {
	kind          codexHostConflictTargetKind
	group         codexHostProcessGroup
	hostPID       int
	appPID        int
	daemonHome    string
	managedSocket string
}

type codexHostProcessProof struct {
	PID         int
	UID         uint32
	PGID        int
	Start       string
	CommandHash string
	ArgsHash    string
}

// codexVerifiedHostConflictTarget carries the complete in-memory proof needed
// to revalidate a process group immediately before a signal or lifecycle stop.
// It is never serialized because start times and command hashes are only useful
// at the instant they are observed.
type codexVerifiedHostConflictTarget struct {
	codexHostConflictTarget
	members []codexHostProcessProof
}

type codexHostConflictPlan struct {
	processes      []codexHostProcessSnapshot
	allowedUIDs    map[uint32]struct{}
	allowedPGIDs   map[int]struct{}
	conflicts      []codexHostConflictTarget
	conflictGroups []codexHostProcessGroup
}

// classifyCodexHostConflictTarget recognizes only process-tree shapes that
// can later receive a stronger lifecycle or identity proof. Everything else
// deliberately remains unknown and cannot be stopped by WeClaw.
func classifyCodexHostConflictTarget(
	processes []codexHostProcessSnapshot,
	group codexHostProcessGroup,
) codexHostConflictTarget {
	target := codexHostConflictTarget{kind: codexHostConflictTargetUnknown, group: group}
	byPID := make(map[int]codexHostProcessSnapshot, len(processes))
	parents := make(map[int]int, len(processes))
	groupMembers := make([]codexHostProcessSnapshot, 0)
	for _, process := range processes {
		byPID[process.PID] = process
		parents[process.PID] = process.PPID
		if process.PGID == group.PGID {
			groupMembers = append(groupMembers, process)
		}
	}
	if len(groupMembers) == 0 {
		return target
	}

	for _, pid := range group.PIDs {
		process, found := byPID[pid]
		if !found {
			return target
		}
		if home, ok := codexOfficialDaemonHomeFromProcess(process); ok {
			trustedTree := true
			for _, member := range groupMembers {
				if member.UID != process.UID || !codexConflictProcessDescendsFrom(member.PID, parents, map[int]struct{}{pid: {}}) {
					trustedTree = false
					break
				}
			}
			if !trustedTree {
				continue
			}
			target.kind = codexHostConflictTargetOfficialDaemon
			target.hostPID = pid
			target.daemonHome = home
			return target
		}
	}

	for _, hostPID := range group.PIDs {
		host, found := byPID[hostPID]
		if !found || !codexPrivateAppHostProcess(host) {
			continue
		}
		for _, candidate := range groupMembers {
			if !codexDesktopAppRootProcess(candidate) ||
				candidate.UID != group.UID || candidate.PGID != group.PGID ||
				!codexConflictProcessDescendsFrom(hostPID, parents, map[int]struct{}{candidate.PID: {}}) {
				continue
			}
			for _, member := range groupMembers {
				if member.UID != group.UID ||
					(member.PID != candidate.PID && !codexConflictProcessDescendsFrom(member.PID, parents, map[int]struct{}{candidate.PID: {}})) {
					return target
				}
			}
			target.kind = codexHostConflictTargetAppPrivate
			target.hostPID = hostPID
			target.appPID = candidate.PID
			return target
		}
	}
	return target
}

func codexConflictProcessDescendsFrom(pid int, parents map[int]int, ancestors map[int]struct{}) bool {
	visited := make(map[int]struct{})
	for pid > 0 {
		if _, ok := ancestors[pid]; ok {
			return true
		}
		if _, ok := visited[pid]; ok {
			return false
		}
		visited[pid] = struct{}{}
		parent, ok := parents[pid]
		if !ok || parent <= 0 || parent == pid {
			return false
		}
		pid = parent
	}
	return false
}

func codexOfficialDaemonHomeFromProcess(process codexHostProcessSnapshot) (string, bool) {
	if !codexAppServerHostProcess(process.Executable, process.Command, process.Args) || len(process.Args) == 0 {
		return "", false
	}
	binary := filepath.Clean(strings.TrimSpace(process.Args[0]))
	if !filepath.IsAbs(binary) || filepath.Base(binary) != "codex" {
		return "", false
	}
	home := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(binary))))
	relative, err := filepath.Rel(home, binary)
	if err != nil || filepath.Clean(relative) != filepath.Join("packages", "standalone", "current", "codex") {
		return "", false
	}
	return home, true
}

func codexPrivateAppHostProcess(process codexHostProcessSnapshot) bool {
	lower := strings.ToLower(process.Command)
	if !strings.Contains(lower, "/chatgpt.app/") && !strings.Contains(lower, "/codex.app/") {
		return false
	}
	return codexAppServerHostProcess(process.Executable, process.Command, process.Args)
}

func codexDesktopAppRootProcess(process codexHostProcessSnapshot) bool {
	lower := strings.ToLower(process.Command)
	if !strings.Contains(lower, "/chatgpt.app/contents/macos/") && !strings.Contains(lower, "/codex.app/contents/macos/") {
		return false
	}
	name := strings.ToLower(filepath.Base(strings.TrimSpace(process.Executable)))
	return name == "chatgpt" || name == "codex"
}

func (target codexHostConflictTarget) restartSnapshot(stopped bool) CodexRestartConflictSnapshot {
	pids := append([]int(nil), target.group.PIDs...)
	return CodexRestartConflictSnapshot{
		Kind: target.group.Kind, PGID: target.group.PGID, PIDs: pids, Stopped: stopped,
	}
}

func restartConflictSnapshots(targets []codexHostConflictTarget, stopped map[int]bool) []CodexRestartConflictSnapshot {
	if len(targets) == 0 {
		return nil
	}
	result := make([]CodexRestartConflictSnapshot, 0, len(targets))
	for _, target := range targets {
		result = append(result, target.restartSnapshot(stopped[target.group.PGID]))
	}
	return result
}

func (a *ACPAgent) inspectRestartCodexHostProcess(pid int) (codexProcessIdentity, error) {
	if a != nil && a.codexHostProcessIdentityCall != nil {
		return a.codexHostProcessIdentityCall(pid)
	}
	return inspectCodexHostProcess(pid)
}

func (a *ACPAgent) stopCodexConflictProcessGroup(ctx context.Context, target codexVerifiedHostConflictTarget) error {
	if a != nil && a.stopCodexConflictProcessGroupCall != nil {
		return a.stopCodexConflictProcessGroupCall(ctx, target)
	}
	return stopCodexConflictProcessGroup(ctx, target)
}

func codexHostArgsHash(args []string) string {
	if len(args) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(sum[:])
}

func sameCodexHostConflictTarget(left, right codexHostConflictTarget) bool {
	return left.kind == right.kind &&
		left.group.PGID == right.group.PGID &&
		left.group.UID == right.group.UID &&
		left.hostPID == right.hostPID &&
		left.appPID == right.appPID &&
		filepath.Clean(left.daemonHome) == filepath.Clean(right.daemonHome) &&
		filepath.Clean(left.managedSocket) == filepath.Clean(right.managedSocket) &&
		sameCodexHostPIDList(left.group.PIDs, right.group.PIDs)
}

func sameCodexHostPIDList(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameCodexHostConflictProof(left, right codexVerifiedHostConflictTarget) bool {
	if !sameCodexHostConflictTarget(left.codexHostConflictTarget, right.codexHostConflictTarget) || len(left.members) != len(right.members) {
		return false
	}
	for index := range left.members {
		if left.members[index] != right.members[index] {
			return false
		}
	}
	return true
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

func (a *ACPAgent) readCodexHostConflictSnapshot(
	ctx context.Context,
) ([]codexHostProcessSnapshot, map[uint32]struct{}, error) {
	snapshot := a.codexHostProcessSnapshotCall
	if snapshot == nil {
		snapshot = systemCodexHostProcessSnapshot
	}
	allowedUIDs, err := a.codexHostConflictAllowedUIDs()
	if err != nil {
		return nil, nil, fmt.Errorf(
			"解析 Codex Host 预检身份失败；本次操作已失败关闭，只读预检未停止任何进程: %w",
			err,
		)
	}
	processes, err := snapshot(ctx, allowedUIDs)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"读取 Codex Host 进程表失败；为避免误启第二个 Host，本次操作已失败关闭，只读预检未停止任何进程: %w",
			err,
		)
	}
	return processes, allowedUIDs, nil
}

func (a *ACPAgent) planCodexHostConflicts(ctx context.Context, authorityPID int) (codexHostConflictPlan, error) {
	processes, allowedUIDs, err := a.readCodexHostConflictSnapshot(ctx)
	if err != nil {
		return codexHostConflictPlan{}, err
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
				return codexHostConflictPlan{}, fmt.Errorf(
					"Codex Host 权威 PID %d 的进程身份无法通过只读预检；本次操作已失败关闭，只读预检未停止任何进程",
					authorityPID,
				)
			}
			authorityPGID = process.PGID
			break
		}
		if authorityPGID == 0 {
			return codexHostConflictPlan{}, fmt.Errorf(
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
			return codexHostConflictPlan{}, fmt.Errorf(
				"Codex Host 权威 PID %d 对应的进程组不包含可验证 app-server；本次操作已失败关闭，只读预检未停止任何进程",
				authorityPID,
			)
		}
	}
	plan := codexHostConflictPlan{
		processes: processes, allowedUIDs: allowedUIDs, allowedPGIDs: allowedPGIDs,
	}
	for _, group := range groups {
		if _, allowed := allowedPGIDs[group.PGID]; allowed {
			continue
		}
		plan.conflictGroups = append(plan.conflictGroups, group)
		target := classifyCodexHostConflictTarget(processes, group)
		if target.kind == codexHostConflictTargetUnknown {
			if managedTarget, ok := a.classifyManagedCodexHostConflictTarget(ctx, processes, group); ok {
				target = managedTarget
			}
		}
		plan.conflicts = append(plan.conflicts, target)
	}
	return plan, nil
}

// classifyManagedCodexHostConflictTarget only trusts metadata that WeClaw
// itself wrote with restrictive permissions. The directory scan is deliberately
// bounded to WeClaw's own runtime directory plus the configured socket; it does
// not search arbitrary user files or infer ownership from a process name.
func (a *ACPAgent) classifyManagedCodexHostConflictTarget(
	ctx context.Context,
	processes []codexHostProcessSnapshot,
	group codexHostProcessGroup,
) (codexHostConflictTarget, bool) {
	if err := ctx.Err(); err != nil {
		return codexHostConflictTarget{}, false
	}
	paths := make([]string, 0, 2)
	if socket, err := a.resolveCodexHostSocket(); err == nil {
		paths = append(paths, socket)
	}
	if dataDir, err := config.DataDir(); err == nil {
		entries, readErr := os.ReadDir(filepath.Join(dataDir, "runtime"))
		if readErr == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid.json") {
					continue
				}
				paths = append(paths, strings.TrimSuffix(filepath.Join(dataDir, "runtime", entry.Name()), ".pid.json"))
			}
		}
	}
	seen := make(map[string]struct{}, len(paths))
	for _, socketPath := range paths {
		socketPath = filepath.Clean(socketPath)
		if _, found := seen[socketPath]; found {
			continue
		}
		seen[socketPath] = struct{}{}
		metadata, err := a.readCodexHostMetadata(socketPath)
		if err != nil || metadata.Manager != codexHostManagerWeClaw || metadata.State != "running" ||
			metadata.PID <= 0 || metadata.ProcessGroupID != metadata.PID || metadata.ProcessGroupID != group.PGID || metadata.UID != group.UID ||
			metadata.CommandFingerprint == "" {
			continue
		}
		parents := make(map[int]int, len(processes))
		for _, candidate := range processes {
			if candidate.PGID != group.PGID {
				continue
			}
			parents[candidate.PID] = candidate.PPID
		}
		trustedTree := true
		for _, candidate := range processes {
			if candidate.PGID != group.PGID {
				continue
			}
			if candidate.UID != group.UID || !codexConflictProcessDescendsFrom(candidate.PID, parents, map[int]struct{}{metadata.PID: {}}) {
				trustedTree = false
				break
			}
		}
		if !trustedTree {
			continue
		}
		for _, process := range processes {
			if process.PID != metadata.PID || process.PGID != group.PGID || process.UID != group.UID {
				continue
			}
			identity, identityErr := a.inspectRestartCodexHostProcess(process.PID)
			if identityErr != nil || identity.uid != metadata.UID || identity.pgid != metadata.ProcessGroupID ||
				identity.start != metadata.ProcessStart || identity.commandHash != metadata.ObservedCommandHash {
				continue
			}
			return codexHostConflictTarget{
				kind:          codexHostConflictTargetManaged,
				group:         group,
				hostPID:       process.PID,
				managedSocket: socketPath,
			}, true
		}
	}
	return codexHostConflictTarget{}, false
}

func (plan codexHostConflictPlan) conflictError() error {
	if len(plan.conflictGroups) == 0 {
		return nil
	}
	return validateCodexHostProcessGroups(plan.conflictGroups, map[int]struct{}{})
}

func (a *ACPAgent) preflightCodexHostConflicts(ctx context.Context, authorityPID int) error {
	if a.codexHostConflictPreflightCall != nil {
		return a.codexHostConflictPreflightCall(ctx, authorityPID)
	}
	plan, err := a.planCodexHostConflicts(ctx, authorityPID)
	if err != nil {
		return err
	}
	return plan.conflictError()
}

// StopConflictingCodexHosts is the explicit offline restart operation. It
// first proves every discovered extra Host, then performs stops one by one.
// The default restart path never calls this method.
func (a *ACPAgent) StopConflictingCodexHosts(ctx context.Context) ([]CodexRestartConflictSnapshot, error) {
	if a == nil || !a.usesCodexSharedHost() {
		return nil, fmt.Errorf("当前 Agent 不是 Codex shared app-server")
	}
	plan, err := a.planCodexHostConflicts(ctx, 0)
	if err != nil {
		return nil, err
	}
	if len(plan.conflictGroups) == 0 {
		return nil, nil
	}
	verified := make([]codexVerifiedHostConflictTarget, 0, len(plan.conflicts))
	for _, target := range plan.conflicts {
		if target.kind == codexHostConflictTargetUnknown {
			return nil, fmt.Errorf(
				"%w：PGID %d（%s）的身份无法完整证明；显式参数不会停止未知进程",
				ErrCodexHostConflict, target.group.PGID, target.group.Kind,
			)
		}
		candidate, verifyErr := a.verifyCodexHostConflictTarget(ctx, target)
		if verifyErr != nil {
			return nil, fmt.Errorf("复核待停止的 Codex Host PGID %d: %w", target.group.PGID, verifyErr)
		}
		verified = append(verified, candidate)
	}
	planned := make([]codexHostConflictTarget, 0, len(verified))
	for _, target := range verified {
		planned = append(planned, target.codexHostConflictTarget)
	}
	stopped := make(map[int]bool, len(verified))
	for _, target := range verified {
		if err := a.stopVerifiedCodexHostConflict(ctx, target); err != nil {
			return nil, fmt.Errorf("%w: 停止冲突 Codex Host PGID %d: %v", ErrCodexRestartUnsafe, target.group.PGID, err)
		}
		stopped[target.group.PGID] = true
	}
	return restartConflictSnapshots(planned, stopped), nil
}

func (a *ACPAgent) captureCodexHostConflictTarget(
	ctx context.Context,
	expected codexHostConflictTarget,
) (codexVerifiedHostConflictTarget, error) {
	processes, allowedUIDs, err := a.readCodexHostConflictSnapshot(ctx)
	if err != nil {
		return codexVerifiedHostConflictTarget{}, err
	}
	groups := collectCodexHostProcessGroups(processes, allowedUIDs)
	var group *codexHostProcessGroup
	for index := range groups {
		if groups[index].PGID == expected.group.PGID {
			group = &groups[index]
			break
		}
	}
	if group == nil {
		return codexVerifiedHostConflictTarget{}, fmt.Errorf("候选 Codex Host PGID %d 已不在进程快照中", expected.group.PGID)
	}
	actual := classifyCodexHostConflictTarget(processes, *group)
	if actual.kind == codexHostConflictTargetUnknown {
		if managedTarget, managedOK := a.classifyManagedCodexHostConflictTarget(ctx, processes, *group); managedOK {
			actual = managedTarget
		}
	}
	if !sameCodexHostConflictTarget(expected, actual) {
		return codexVerifiedHostConflictTarget{}, fmt.Errorf("候选 Codex Host PGID %d 的命令、进程组或父子关系已变化", expected.group.PGID)
	}
	members := make([]codexHostProcessProof, 0)
	for _, process := range processes {
		if process.PGID != expected.group.PGID {
			continue
		}
		if _, allowed := allowedUIDs[process.UID]; !allowed {
			return codexVerifiedHostConflictTarget{}, fmt.Errorf("候选 Codex Host PGID %d 包含不受信任 UID", expected.group.PGID)
		}
		identity, identityErr := a.inspectRestartCodexHostProcess(process.PID)
		if identityErr != nil {
			return codexVerifiedHostConflictTarget{}, fmt.Errorf("复核候选 Codex Host PID %d: %w", process.PID, identityErr)
		}
		if identity.uid != process.UID || identity.pgid != process.PGID || identity.start == "" || identity.commandHash == "" {
			return codexVerifiedHostConflictTarget{}, fmt.Errorf("候选 Codex Host PID %d 的 UID、PGID、启动时间或命令指纹不一致", process.PID)
		}
		proof := codexHostProcessProof{
			PID: process.PID, UID: identity.uid, PGID: identity.pgid,
			Start: identity.start, CommandHash: identity.commandHash,
		}
		if process.PID == expected.hostPID {
			proof.ArgsHash = codexHostArgsHash(process.Args)
			if proof.ArgsHash == "" {
				return codexVerifiedHostConflictTarget{}, fmt.Errorf("候选 Codex Host PID %d 缺少原始参数", process.PID)
			}
		}
		members = append(members, proof)
	}
	if len(members) == 0 {
		return codexVerifiedHostConflictTarget{}, fmt.Errorf("候选 Codex Host PGID %d 缺少可复核成员", expected.group.PGID)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].PID < members[j].PID })
	return codexVerifiedHostConflictTarget{codexHostConflictTarget: actual, members: members}, nil
}

func (a *ACPAgent) verifyCodexHostConflictTarget(
	ctx context.Context,
	target codexHostConflictTarget,
) (codexVerifiedHostConflictTarget, error) {
	verified, err := a.captureCodexHostConflictTarget(ctx, target)
	if err != nil {
		return codexVerifiedHostConflictTarget{}, err
	}
	switch verified.kind {
	case codexHostConflictTargetAppPrivate:
		return verified, nil
	case codexHostConflictTargetManaged:
		return verified, nil
	case codexHostConflictTargetOfficialDaemon:
		if err := a.verifyOfficialDaemonConflict(ctx, verified); err != nil {
			return codexVerifiedHostConflictTarget{}, err
		}
		return verified, nil
	default:
		return codexVerifiedHostConflictTarget{}, fmt.Errorf(
			"Codex Host %s（PGID %d）的管理身份无法证明；显式参数不会停止未知进程",
			verified.group.Kind, verified.group.PGID,
		)
	}
}

func (a *ACPAgent) stopVerifiedCodexHostConflict(
	ctx context.Context,
	expected codexVerifiedHostConflictTarget,
) error {
	current, err := a.verifyCodexHostConflictTarget(ctx, expected.codexHostConflictTarget)
	if err != nil {
		return err
	}
	if !sameCodexHostConflictProof(expected, current) {
		return fmt.Errorf("候选 Codex Host PGID %d 的 PID、UID、启动时间或命令指纹已变化", expected.group.PGID)
	}
	if a.stopConflictingCodexHostCall != nil {
		return a.stopConflictingCodexHostCall(ctx, current)
	}
	switch current.kind {
	case codexHostConflictTargetAppPrivate:
		return stopCodexConflictProcessGroup(ctx, current)
	case codexHostConflictTargetManaged:
		lock, err := a.acquireCodexHostStartupLock(ctx, current.managedSocket)
		if err != nil {
			return fmt.Errorf("锁定 WeClaw Host metadata: %w", err)
		}
		defer releaseCodexHostStartupLock(lock)
		lockedCurrent, err := a.verifyCodexHostConflictTarget(ctx, expected.codexHostConflictTarget)
		if err != nil || !sameCodexHostConflictProof(expected, lockedCurrent) {
			if err != nil {
				return fmt.Errorf("锁内复核 WeClaw Host: %w", err)
			}
			return fmt.Errorf("锁内复核 WeClaw Host PGID %d 的身份已变化", current.group.PGID)
		}
		if err := stopCodexConflictProcessGroup(ctx, lockedCurrent); err != nil {
			return err
		}
		metadata, err := a.readCodexHostMetadata(lockedCurrent.managedSocket)
		if err != nil {
			return fmt.Errorf("读取已停止 WeClaw Host metadata: %w", err)
		}
		if metadata.PID != lockedCurrent.hostPID || metadata.ProcessGroupID != lockedCurrent.group.PGID {
			return fmt.Errorf("WeClaw Host metadata 在停止后发生身份变化")
		}
		if err := a.markCodexHostMetadataStoppedLocked(current.managedSocket, metadata); err != nil {
			return fmt.Errorf("记录 WeClaw Host 已停止: %w", err)
		}
		return nil
	case codexHostConflictTargetOfficialDaemon:
		return a.stopOfficialDaemonConflict(ctx, current)
	default:
		return fmt.Errorf("Codex Host PGID %d 不支持受控停止", current.group.PGID)
	}
}

func (a *ACPAgent) verifyOfficialDaemonConflict(ctx context.Context, target codexVerifiedHostConflictTarget) error {
	configuredHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return fmt.Errorf("解析 official daemon CODEX_HOME: %w", err)
	}
	if filepath.Clean(configuredHome) != filepath.Clean(target.daemonHome) {
		return fmt.Errorf("official daemon CODEX_HOME 与当前配置不一致，拒绝停止 PGID %d", target.group.PGID)
	}
	socketPath := codexDaemonSocketPath(configuredHome)
	output, err := a.runAndValidateCodexDaemonLifecycle(ctx, "version", socketPath)
	if err != nil {
		return fmt.Errorf("复核 official daemon PGID %d: %w", target.group.PGID, err)
	}
	if output.PID != target.hostPID {
		return fmt.Errorf("official daemon PID 已从 %d 变为 %d", target.hostPID, output.PID)
	}
	return nil
}

func (a *ACPAgent) stopOfficialDaemonConflict(ctx context.Context, target codexVerifiedHostConflictTarget) error {
	configuredHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return fmt.Errorf("解析 official daemon CODEX_HOME: %w", err)
	}
	socketPath := codexDaemonSocketPath(configuredHome)
	version, err := a.runAndValidateCodexDaemonLifecycle(ctx, "version", socketPath)
	if err != nil {
		return fmt.Errorf("停止前复核 official daemon PGID %d: %w", target.group.PGID, err)
	}
	if version.PID != target.hostPID {
		return fmt.Errorf("official daemon 停止前 PID 已从 %d 变为 %d", target.hostPID, version.PID)
	}
	if _, err := a.runAndValidateCodexDaemonLifecycle(ctx, "stop", socketPath); err != nil {
		if !errors.Is(err, errCodexDaemonUnmanaged) {
			return fmt.Errorf("停止 official daemon PGID %d: %w", target.group.PGID, err)
		}
		if fallbackErr := a.stopOfficialDaemonProcessGroupFallback(ctx, target, configuredHome, socketPath); fallbackErr != nil {
			return fmt.Errorf("停止 official daemon PGID %d: lifecycle 不可用且受保护进程组停止失败: %w", target.group.PGID, fallbackErr)
		}
		return nil
	}
	start := ""
	for _, member := range target.members {
		if member.PID == target.hostPID {
			start = member.Start
			break
		}
	}
	if start == "" {
		return fmt.Errorf("official daemon PGID %d 缺少启动时间证明", target.group.PGID)
	}
	if err := a.verifyCodexDaemonStopped(ctx, socketPath, codexHostMetadata{
		PID: target.hostPID, ProcessStart: start,
	}); err != nil {
		return fmt.Errorf("确认 official daemon PGID %d 已停止: %w", target.group.PGID, err)
	}
	if err := waitCodexConflictMembersExit(ctx, target.members, acpKillGrace); err != nil {
		return fmt.Errorf("等待 official daemon PGID %d 退出: %w", target.group.PGID, err)
	}
	return nil
}

// stopOfficialDaemonProcessGroupFallback handles standalone releases whose
// protected daemon record exists but whose CLI lifecycle stop rejects the
// already-running app-server as unmanaged. The fallback remains narrower than
// a name-based kill: it requires the same protected metadata and a fresh full
// process-tree proof while holding the shared lifecycle lock.
func (a *ACPAgent) stopOfficialDaemonProcessGroupFallback(
	ctx context.Context,
	expected codexVerifiedHostConflictTarget,
	codexHome string,
	socketPath string,
) error {
	lock, err := a.acquireCodexHostStartupLock(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("锁定 official daemon metadata: %w", err)
	}
	defer releaseCodexHostStartupLock(lock)

	metadata, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		return fmt.Errorf("读取 official daemon protected metadata: %w", err)
	}
	expectedPath := filepath.Clean(codexDaemonManagedBinaryPath(codexHome))
	if metadata.Manager != codexHostManagerDaemon || metadata.State != "running" ||
		metadata.ManagedCodexPath == "" || filepath.Clean(metadata.ManagedCodexPath) != expectedPath ||
		metadata.PID != expected.hostPID || metadata.ProcessGroupID != expected.group.PGID ||
		metadata.ProcessGroupID != metadata.PID || metadata.UID != expected.group.UID {
		return fmt.Errorf("official daemon protected metadata 与 PGID %d 不一致", expected.group.PGID)
	}

	current, err := a.captureCodexHostConflictTarget(ctx, expected.codexHostConflictTarget)
	if err != nil {
		return fmt.Errorf("重新复核 official daemon 进程树: %w", err)
	}
	if !sameCodexHostConflictProof(expected, current) {
		return fmt.Errorf("official daemon PGID %d 的进程身份已变化", expected.group.PGID)
	}
	hostProof := codexHostProcessProof{}
	for _, member := range current.members {
		if member.PID == current.hostPID {
			hostProof = member
			break
		}
	}
	if hostProof.PID == 0 || metadata.ProcessStart != hostProof.Start || metadata.ObservedCommandHash != hostProof.CommandHash {
		return fmt.Errorf("official daemon metadata 的启动时间或命令哈希与进程不一致")
	}

	if err := a.stopCodexConflictProcessGroup(ctx, current); err != nil {
		return err
	}
	if err := a.verifyCodexDaemonStopped(ctx, socketPath, codexHostMetadata{
		PID: hostProof.PID, ProcessStart: hostProof.Start,
	}); err != nil {
		return fmt.Errorf("确认 official daemon 已停止: %w", err)
	}
	stopped, err := a.readCodexHostMetadata(socketPath)
	if err != nil {
		return fmt.Errorf("读取停止后的 official daemon metadata: %w", err)
	}
	if stopped.PID != metadata.PID || stopped.ProcessGroupID != metadata.ProcessGroupID ||
		stopped.ProcessStart != metadata.ProcessStart || stopped.ObservedCommandHash != metadata.ObservedCommandHash {
		return fmt.Errorf("official daemon metadata 在停止后发生身份变化")
	}
	if err := a.markCodexHostMetadataStoppedLocked(socketPath, metadata); err != nil {
		return fmt.Errorf("记录 official daemon 已停止: %w", err)
	}
	return nil
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
