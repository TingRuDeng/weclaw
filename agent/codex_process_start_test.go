package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSameCodexProcessStart(t *testing.T) {
	const english = "Tue Oct  6 18:33:39 2026"
	tests := []struct {
		name, left, right string
		want              bool
	}{
		{"legacy Chinese", "二 10/ 6 18:33:39 2026", english, true},
		{"double digit day", "二 10/13 21:33:05 2026", "Tue Oct 13 21:33:05 2026", true},
		{"single digit month", "四  1/ 1 00:00:00 2026", "Thu Jan  1 00:00:00 2026", true},
		{"same raw", english, english, true},
		{"empty", "", "", false},
		{"whitespace", " ", " ", false},
		{"second changed", "二 10/ 6 18:33:40 2026", english, false},
		{"date changed", "三 10/ 7 18:33:39 2026", english, false},
		{"month changed", "五 11/ 6 18:33:39 2026", english, false},
		{"year changed", "三 10/ 6 18:33:39 2027", english, false},
		{"timezone shift is not guessed", "二 10/ 6 10:33:39 2026", english, false},
		{"wrong Chinese weekday", "三 10/ 6 18:33:39 2026", english, false},
		{"wrong English weekday", "二 10/ 6 18:33:39 2026", "Wed Oct  6 18:33:39 2026", false},
		{"invalid date", "二 13/ 6 18:33:39 2026", english, false},
		{"invalid time", "二 10/ 6 28:33:39 2026", english, false},
		{"unknown locale", "Di Okt  6 18:33:39 2026", english, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameCodexProcessStart(tc.left, tc.right); got != tc.want {
				t.Fatalf("sameCodexProcessStart(%q, %q)=%v, want %v", tc.left, tc.right, got, tc.want)
			}
			if got := sameCodexProcessStart(tc.right, tc.left); got != tc.want {
				t.Fatalf("reverse comparison=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestInspectCodexHostProcessLocaleIndependent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("regression for macOS localized ps output")
	}
	t.Setenv("LC_ALL", "C")
	english, err := inspectCodexHostProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	chinese, err := inspectCodexHostProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if english != chinese {
		t.Fatalf("same process has different identity across locales: C=%#v zh_CN=%#v", english, chinese)
	}
}

func TestCodexHostSupervisorAcceptsLegacyChineseStart(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS Chinese ps format")
	}
	t.Setenv("LC_ALL", "C")
	dir, err := os.MkdirTemp("/tmp", "weclaw-locale-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "codex.sock")
	a := NewACPAgent(ACPAgentConfig{
		ConfiguredName: "codex", Command: "codex", Args: []string{"app-server"}, AppServerSocket: socketPath,
	})
	child := exec.CommandContext(context.Background(), "sleep", "30")
	configureACPProcess(child)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	identity, err := inspectCodexHostProcess(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	legacyCommand := exec.Command("ps", "-p", strconv.Itoa(child.Process.Pid), "-o", "lstart=")
	legacyCommand.Env = append(os.Environ(), "LC_ALL=zh_CN.UTF-8")
	legacyOutput, err := legacyCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	legacyStart := strings.TrimSpace(string(legacyOutput))
	if !codexChineseProcessStart.MatchString(legacyStart) || legacyStart == identity.start {
		t.Fatalf("expected a distinct Chinese legacy timestamp, got %q and live %q", legacyStart, identity.start)
	}
	metadata := codexHostMetadata{
		Version: codexHostMetadataVersion, State: "running", PID: child.Process.Pid,
		ProcessGroupID: identity.pgid, UID: identity.uid, ProcessStart: legacyStart,
		ObservedCommandHash: identity.commandHash, CommandFingerprint: a.configuredCodexHostCommandFingerprint(socketPath),
		SocketPath: socketPath, Generation: 4, StartedAt: time.Now().UTC(),
	}
	if err := a.writeCodexHostMetadata(socketPath, metadata); err != nil {
		t.Fatal(err)
	}
	if status := a.InspectCodexHost(context.Background()); !status.Managed || !status.Running {
		t.Fatalf("legacy metadata rejected: %#v", status)
	}
	for _, field := range []string{"uid", "pgid", "command", "start"} {
		changed := metadata
		switch field {
		case "uid":
			changed.UID++
		case "pgid":
			changed.ProcessGroupID++
		case "command":
			changed.ObservedCommandHash = "different-command"
		case "start":
			changed.ProcessStart = "二 10/ 6 00:00:00 1999"
		}
		if err := a.writeCodexHostMetadata(socketPath, changed); err != nil {
			t.Fatal(err)
		}
		if _, err := a.validateManagedCodexHost(socketPath); err == nil {
			t.Fatalf("changed %s must be rejected", field)
		}
	}
}
