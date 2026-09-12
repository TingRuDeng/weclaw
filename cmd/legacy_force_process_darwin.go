//go:build darwin

package cmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func inspectLegacyProcess(pid int) (legacyProcessIdentity, []string, error) {
	var identity legacyProcessIdentity
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if unix.Kill(pid, 0) == unix.ESRCH {
			return identity, nil, os.ErrNotExist
		}
		return identity, nil, err
	}
	if info.Proc.P_pid == 0 || info.Proc.P_stat == 5 {
		return identity, nil, os.ErrNotExist
	}
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if unix.Kill(pid, 0) == unix.ESRCH {
			return identity, nil, os.ErrNotExist
		}
		return identity, nil, err
	}
	executable, args, err := parseLegacyDarwinArgs(data)
	if err != nil {
		return identity, nil, err
	}
	identity = legacyProcessIdentity{pid: pid, uid: int(info.Eproc.Ucred.Uid), pgid: int(info.Eproc.Pgid), executable: executable,
		startedAt: time.Unix(info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec)*1000), fingerprint: legacyArgsFingerprint(args)}
	return identity, args, nil
}

func parseLegacyDarwinArgs(data []byte) (string, []string, error) {
	if len(data) < 4 {
		return "", nil, fmt.Errorf("进程原始参数不完整")
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	if argc <= 0 || argc > 1<<16 {
		return "", nil, fmt.Errorf("进程参数数量无效")
	}
	data = data[4:]
	end := bytes.IndexByte(data, 0)
	if end <= 0 {
		return "", nil, fmt.Errorf("进程 executable 不完整")
	}
	executable := string(data[:end])
	data = bytes.TrimLeft(data[end+1:], "\x00")
	args := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return "", nil, fmt.Errorf("进程 argv 不完整")
		}
		args = append(args, string(data[:end]))
		data = data[end+1:]
	}
	return executable, args, nil
}

func legacyServiceUsesSystemd(state runtimeState, _ legacyProcessIdentity) (bool, error) {
	if state.Mode == "systemd" {
		return false, fmt.Errorf("当前平台不支持 systemd 迁移")
	}
	return false, nil
}

func stopLegacySystemdService(context.Context, legacyServiceTarget) error {
	return fmt.Errorf("当前平台不支持 systemd")
}
