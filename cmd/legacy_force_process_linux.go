//go:build linux

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func inspectLegacyProcess(pid int) (legacyProcessIdentity, []string, error) {
	var identity legacyProcessIdentity
	dir := "/proc/" + strconv.Itoa(pid)
	var stat unix.Stat_t
	if err := unix.Stat(dir, &stat); err != nil {
		return identity, nil, err
	}
	raw, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return identity, nil, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return identity, nil, fmt.Errorf("进程 stat 无效")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return identity, nil, fmt.Errorf("进程 stat 不完整")
	}
	if fields[0] == "Z" {
		return identity, nil, os.ErrNotExist
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return identity, nil, err
	}
	executable, err := os.Readlink(dir + "/exe")
	if err != nil {
		return identity, nil, err
	}
	raw, err = os.ReadFile(dir + "/cmdline")
	if err != nil {
		return identity, nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return identity, nil, fmt.Errorf("进程 argv 不完整")
	}
	args := strings.Split(string(raw[:len(raw)-1]), "\x00")
	command := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	started, err := command.Output()
	if err != nil {
		return identity, nil, err
	}
	startedAt, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(string(started)), " "), time.Local)
	if err != nil {
		return identity, nil, err
	}
	// ticks 纳入指纹，避免 ps 秒级时间相同的 PID 复用通过复核。
	fingerprintArgs := append(append([]string(nil), args...), fields[19])
	identity = legacyProcessIdentity{pid: pid, uid: int(stat.Uid), pgid: pgid, executable: executable, startedAt: startedAt, fingerprint: legacyArgsFingerprint(fingerprintArgs)}
	return identity, args, nil
}

func legacyServiceUsesSystemd(state runtimeState, _ legacyProcessIdentity) (bool, error) {
	cgroup, err := os.ReadFile("/proc/" + strconv.Itoa(state.PID) + "/cgroup")
	if err != nil {
		return false, err
	}
	managed := state.Mode == "systemd"
	for _, line := range strings.Split(string(cgroup), "\n") {
		for _, part := range strings.Split(line, "/") {
			if strings.HasSuffix(part, ".service") && !strings.HasPrefix(part, "user@") {
				if part != "weclaw.service" {
					return false, fmt.Errorf("旧服务由其他 systemd unit 管理，请通过对应服务管理器停止")
				}
				managed = true
			}
		}
	}
	if managed {
		if err := verifyLegacySystemdUnit(state.PID); err != nil {
			return false, err
		}
	}
	return managed, nil
}

func verifyLegacySystemdUnit(pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "systemctl", "show", "weclaw.service", "--property=MainPID,KillMode").Output()
	if err != nil {
		return fmt.Errorf("读取旧服务 systemd 身份: %w", err)
	}
	properties := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[key] = value
		}
	}
	if properties["MainPID"] != strconv.Itoa(pid) || properties["KillMode"] != "process" {
		return fmt.Errorf("systemd MainPID 或 KillMode=process 未确认，拒绝连带停止其他进程")
	}
	return nil
}

func stopLegacySystemdService(ctx context.Context, target legacyServiceTarget) error {
	if err := verifyLegacySystemdUnit(target.state.PID); err != nil {
		return err
	}
	name, args := "systemctl", []string{"stop", "weclaw.service"}
	if os.Geteuid() != 0 {
		name, args = "sudo", append([]string{"systemctl"}, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}
