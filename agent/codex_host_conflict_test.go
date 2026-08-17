package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseNullTerminatedCodexHostArgs(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    []string
		wantErr bool
	}{
		{
			name: "preserves spaces",
			data: []byte("/opt/codex\x00-C\x00/tmp/path with spaces"),
			want: []string{"/opt/codex", "-C", "/tmp/path with spaces"},
		},
		{
			name: "drops only the trailing terminator",
			data: []byte("/opt/codex\x00app-server\x00"),
			want: []string{"/opt/codex", "app-server"},
		},
		{
			name:    "rejects empty input",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseNullTerminatedCodexHostArgs(test.data)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseNullTerminatedCodexHostArgs(%q) error=nil, want failure", test.data)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args=%#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCodexHostConflictGroupsNodeWrapperAndNativeChildByPGID(t *testing.T) {
	processes, err := parseCodexHostProcessSnapshot(strings.Join([]string{
		"100 1 100 501 node node /opt/codex/bin/codex app-server --listen unix:///tmp/codex.sock",
		"101 100 100 501 codex /opt/codex/vendor/codex app-server --listen unix:///tmp/codex.sock",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}

	groups := collectCodexHostProcessGroups(processes, map[uint32]struct{}{501: {}})
	if len(groups) != 1 {
		t.Fatalf("groups=%#v, want one Host process group", groups)
	}
	if groups[0].PGID != 100 || len(groups[0].PIDs) != 2 || groups[0].PIDs[0] != 100 || groups[0].PIDs[1] != 101 {
		t.Fatalf("group=%#v, want pgid=100 pids=[100 101]", groups[0])
	}
}

func TestCodexHostConflictAllowsVerifiedAuthorityGroup(t *testing.T) {
	processes := []codexHostProcessSnapshot{{
		PID: 100, PPID: 1, PGID: 100, UID: 501,
		Command: "/opt/codex/codex app-server --listen unix:///tmp/codex.sock",
	}}

	err := validateCodexHostProcessGroups(
		collectCodexHostProcessGroups(processes, map[uint32]struct{}{501: {}}),
		map[int]struct{}{100: {}},
	)
	if err != nil {
		t.Fatalf("verified authority must pass preflight: %v", err)
	}
}

func TestCodexHostConflictAllowsVerifiedWrapperAuthorityGroup(t *testing.T) {
	uid := uint32(os.Geteuid())
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return []codexHostProcessSnapshot{
			{
				PID: 100, PPID: 1, PGID: 100, UID: uid,
				Command: "sudo -n -u codex -- /opt/codex app-server --listen unix:///tmp/codex.sock",
			},
			{
				PID: 101, PPID: 100, PGID: 100, UID: uid,
				Command: "/opt/codex app-server --listen unix:///tmp/codex.sock",
			},
		}, nil
	}

	if err := a.preflightCodexHostConflicts(context.Background(), 100); err != nil {
		t.Fatalf("verified wrapper authority group must pass preflight: %v", err)
	}
}

func TestCodexHostConflictUIDSetIncludesSudoWrapper(t *testing.T) {
	allowed := codexHostConflictUIDSet(501, 1001, true)
	for _, uid := range []uint32{0, 501, 1001} {
		if _, ok := allowed[uid]; !ok {
			t.Fatalf("allowed UIDs=%v, want sudo wrapper/current/target uid %d", allowed, uid)
		}
	}
}

func TestCodexHostProcessArgsGoneOnlyAcceptsExitedProcess(t *testing.T) {
	for _, err := range []error{os.ErrNotExist, syscall.ESRCH} {
		if !codexHostProcessArgsGone(err) {
			t.Fatalf("error=%v, want exited process classification", err)
		}
	}
	if codexHostProcessArgsGone(os.ErrPermission) {
		t.Fatal("permission failure must remain fail-closed")
	}
}

func TestCodexHostConflictRejectsAdditionalUnknownGroupWithoutLeakingCommand(t *testing.T) {
	processes := []codexHostProcessSnapshot{
		{PID: 100, PPID: 1, PGID: 100, UID: 501, Command: "/opt/codex/codex app-server --listen unix:///tmp/authority.sock"},
		{PID: 200, PPID: 1, PGID: 200, UID: 501, Command: "/usr/local/bin/codex app-server --listen unix:///tmp/other.sock --token super-secret"},
	}

	err := validateCodexHostProcessGroups(
		collectCodexHostProcessGroups(processes, map[uint32]struct{}{501: {}}),
		map[int]struct{}{100: {}},
	)
	if err == nil {
		t.Fatal("additional app-server process group must fail closed")
	}
	message := err.Error()
	for _, want := range []string{"PGID 200", "未停止任何进程", "手动确认"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error=%q, want %q", message, want)
		}
	}
	for _, secret := range []string{"super-secret", "/tmp/other.sock", "/usr/local/bin/codex"} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked command detail %q: %s", secret, message)
		}
	}
}

func TestCodexHostConflictFailsClosedWhenProcessSnapshotFails(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return nil, errors.New("process table unavailable")
	}

	err := a.preflightCodexHostConflicts(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "读取 Codex Host 进程表") || !strings.Contains(err.Error(), "process table unavailable") {
		t.Fatalf("error=%v, want observable fail-closed snapshot error", err)
	}
}

func TestCodexHostConflictRejectsConnectedManagedHostWithoutMetadata(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}})
	a.codexHostProcessSnapshotCall = func(context.Context, map[uint32]struct{}) ([]codexHostProcessSnapshot, error) {
		return []codexHostProcessSnapshot{{
			PID: 1, PPID: 0, PGID: 1, UID: uint32(os.Geteuid()),
			Executable: "launchd", Command: "launchd",
		}}, nil
	}

	err := a.preflightConnectedManagedCodexHost(
		context.Background(),
		filepath.Join(t.TempDir(), "managed.sock"),
	)
	if err == nil || !strings.Contains(err.Error(), "权威身份无法确认") || !strings.Contains(err.Error(), "未停止任何进程") {
		t.Fatalf("error=%v, want fail-closed missing metadata error", err)
	}
}

func TestCodexHostConflictIgnoresNearMatchesRemoteCLIAndOtherUsers(t *testing.T) {
	processes := []codexHostProcessSnapshot{
		{PID: 10, PPID: 1, PGID: 10, UID: 501, Command: "codex app-server-malicious --listen unix:///tmp/a"},
		{PID: 11, PPID: 1, PGID: 11, UID: 501, Command: "codex --remote unix:///tmp/daemon.sock"},
		{PID: 12, PPID: 1, PGID: 12, UID: 501, Command: "codex app-server daemon version"},
		{PID: 13, PPID: 1, PGID: 13, UID: 501, Command: "codex app-server proxy"},
		{PID: 14, PPID: 1, PGID: 14, UID: 501, Command: "python worker.py codex app-server"},
		{PID: 15, PPID: 1, PGID: 15, UID: 502, Command: "codex app-server --listen unix:///tmp/other-user.sock"},
	}

	groups := collectCodexHostProcessGroups(processes, map[uint32]struct{}{501: {}})
	if len(groups) != 0 {
		t.Fatalf("groups=%#v, want no Host conflicts", groups)
	}
}

func TestCodexHostConflictMatchesOnlyRealAppServerSubcommand(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		command    string
		args       []string
		want       bool
	}{
		{
			name:    "native executable path contains spaces",
			command: "/Users/Test User/bin/codex -c feature=true app-server --listen stdio://",
			want:    true,
		},
		{
			name:    "node script path contains spaces",
			command: "node /Users/Test User/lib/node_modules/@openai/codex/bin/codex app-server --listen stdio://",
			want:    true,
		},
		{
			name:    "exec argument resembles subcommand",
			command: "codex exec app-server",
			want:    false,
		},
		{
			name:    "remote frontend argument resembles subcommand",
			command: "codex --remote unix:///tmp/codex.sock app-server",
			want:    false,
		},
		{
			name:    "value-taking global option before app-server",
			command: "codex --remote-auth-token-env TEST_TOKEN app-server --listen stdio://",
			want:    true,
		},
		{
			name:       "exact argv preserves spaces in global option value",
			executable: "codex",
			command:    "codex -C /tmp/path with spaces app-server --listen stdio://",
			args:       []string{"/opt/codex", "-C", "/tmp/path with spaces", "app-server", "--listen", "stdio://"},
			want:       true,
		},
		{
			name:       "variadic image values do not become app-server subcommand",
			executable: "codex",
			command:    "codex --image screenshot.png app-server --help",
			args:       []string{"/opt/codex", "--image", "screenshot.png", "app-server", "--help"},
			want:       false,
		},
		{
			name:    "option terminator prevents app-server subcommand",
			command: "codex -- app-server --listen stdio://",
			want:    false,
		},
		{
			name:    "app-server help is not a Host",
			command: "codex app-server --help",
			want:    false,
		},
		{
			name:    "app-server tooling subcommands are not Hosts",
			command: "codex app-server -c feature=true generate-json-schema --out /tmp/schema",
			want:    false,
		},
		{
			name:       "unknown app-server option before tooling fails closed",
			executable: "codex",
			command:    "codex app-server --future-label daemon",
			args:       []string{"/opt/codex", "app-server", "--future-label", "daemon"},
			want:       true,
		},
		{
			name:       "non Codex executable carries a Codex-looking argument",
			executable: "python3",
			command:    "python3 worker.py /tmp/codex app-server",
			want:       false,
		},
		{
			name:       "node worker argument is not a Codex launcher",
			executable: "node",
			command:    "node worker.js /tmp/codex app-server",
			args:       []string{"node", "worker.js", "/tmp/codex", "app-server"},
			want:       false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := codexAppServerHostProcess(test.executable, test.command, test.args); got != test.want {
				t.Fatalf("codexAppServerHostProcess(%q, %q, %#v)=%v, want %v", test.executable, test.command, test.args, got, test.want)
			}
		})
	}
}

func TestCodexHostConflictRejectsMalformedProcessSnapshot(t *testing.T) {
	_, err := parseCodexHostProcessSnapshot("100 invalid-row")
	if err == nil || !strings.Contains(err.Error(), "解析 Codex Host 进程表") {
		t.Fatalf("error=%v, want malformed snapshot failure", err)
	}
}

func TestCodexHostConflictBlocksManagedStartBeforeProcessLaunch(t *testing.T) {
	dir := newShortCodexHome(t)
	socketPath := filepath.Join(dir, "managed.sock")
	a := NewACPAgent(ACPAgentConfig{
		Command: "/command/must/not/run/codex", Args: []string{"app-server"},
		AppServerSocket: socketPath, CodexHostMode: codexHostModeManaged,
	})
	want := errors.New("conflicting Host")
	a.codexHostConflictPreflightCall = func(_ context.Context, authorityPID int) error {
		if authorityPID != 0 {
			t.Fatalf("authority PID=%d, want empty authority before managed start", authorityPID)
		}
		return want
	}

	_, err := a.launchCodexHostClientLocked(context.Background(), socketPath)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want conflict before process launch", err)
	}
}

func TestCodexHostConflictBlocksDaemonStartBeforeLifecycleMutation(t *testing.T) {
	a, socketPath := newCodexDaemonTestAgent(t)
	want := errors.New("conflicting Host")
	a.codexHostConflictPreflightCall = func(_ context.Context, authorityPID int) error {
		if authorityPID != 0 {
			t.Fatalf("authority PID=%d, want empty authority before daemon start", authorityPID)
		}
		return want
	}
	a.codexDaemonLifecycleCall = func(context.Context, string) (codexDaemonLifecycleOutput, error) {
		t.Fatal("daemon lifecycle must not run after conflict preflight fails")
		return codexDaemonLifecycleOutput{}, nil
	}

	_, err := a.launchCodexDaemonClientLocked(context.Background(), socketPath)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want conflict before daemon lifecycle", err)
	}
}

func TestCodexHostConflictBlocksRestartBeforeIntentAndStop(t *testing.T) {
	a, _, cleanup := newManagedRestartFixture(t, 11)
	defer cleanup()
	want := errors.New("conflicting Host")
	a.codexHostConflictPreflightCall = func(_ context.Context, authorityPID int) error {
		if authorityPID <= 0 {
			t.Fatalf("authority PID=%d, want verified managed Host", authorityPID)
		}
		return want
	}
	persisted := false
	stopped := false
	a.stopManagedHostCall = func(context.Context, string) error {
		stopped = true
		return nil
	}

	_, err := a.PrepareCodexRestart(context.Background(), func(CodexRestartSnapshot) error {
		persisted = true
		return nil
	})
	if !errors.Is(err, want) || persisted || stopped {
		t.Fatalf("error=%v persisted=%v stopped=%v, want read-only rejection", err, persisted, stopped)
	}
}

func TestCodexHostConflictBlocksExistingHostAttachBeforeDial(t *testing.T) {
	a, socketPath, cleanup := newManagedRestartFixture(t, 12)
	defer cleanup()
	want := errors.New("conflicting Host")
	a.codexHostConflictPreflightCall = func(_ context.Context, authorityPID int) error {
		if authorityPID <= 0 {
			t.Fatalf("authority PID=%d, want verified managed Host", authorityPID)
		}
		return want
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := a.attachExistingSharedCodexHost(ctx, socketPath)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want conflict before existing Host dial", err)
	}
}

func TestCodexHostConflictCLILaunchChecksBeforeAndAfterDaemonStart(t *testing.T) {
	home := newShortCodexHome(t)
	managedCodex := codexDaemonManagedBinaryPath(home)
	if err := os.MkdirAll(filepath.Dir(managedCodex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedCodex, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex", Args: []string{"app-server"}, CodexHostMode: codexHostModeDaemon,
		Env: map[string]string{"CODEX_HOME": home},
	})
	var authorityPIDs []int
	a.codexHostConflictPreflightCall = func(_ context.Context, authorityPID int) error {
		authorityPIDs = append(authorityPIDs, authorityPID)
		return nil
	}
	a.codexDaemonLifecycleCall = func(_ context.Context, action string) (codexDaemonLifecycleOutput, error) {
		if action != "start" {
			t.Fatalf("action=%q, want start", action)
		}
		return codexDaemonLifecycleOutput{
			Status: "started", Backend: "pid", PID: 321,
			ManagedCodexPath: managedCodex, SocketPath: codexDaemonSocketPath(home),
		}, nil
	}

	if _, err := a.PrepareCodexCLILaunch(context.Background(), CodexCLILaunchOptions{AllowHostStart: true}); err != nil {
		t.Fatal(err)
	}
	if len(authorityPIDs) != 2 || authorityPIDs[0] != 0 || authorityPIDs[1] != 321 {
		t.Fatalf("preflight authority PIDs=%v, want [0 321]", authorityPIDs)
	}
}
