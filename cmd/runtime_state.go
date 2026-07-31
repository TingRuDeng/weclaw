package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

const daemonChildEnv = "WECLAW_DAEMON_CHILD"

type runtimeState struct {
	PID       int       `json:"pid"`
	Exe       string    `json:"exe,omitempty"`
	Version   string    `json:"version,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// readRuntimeState 同时兼容旧版纯数字 pid 文件和新版 JSON 状态文件。
func readRuntimeState() (runtimeState, error) {
	path, err := resolveWeclawFile("weclaw.pid")
	if err != nil {
		return runtimeState{}, err
	}
	if err := securefile.EnsureDir(filepath.Dir(path)); err != nil {
		return runtimeState{}, err
	}
	data, err := securefile.Read(path)
	if err != nil {
		return runtimeState{}, err
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "{") {
		var state runtimeState
		if err := json.Unmarshal(data, &state); err != nil {
			return runtimeState{}, err
		}
		if state.PID <= 0 {
			return runtimeState{}, fmt.Errorf("runtime state missing pid")
		}
		return state, nil
	}
	pid, err := strconv.Atoi(text)
	if err != nil {
		return runtimeState{}, err
	}
	return runtimeState{PID: pid}, nil
}

// writeRuntimeState 写入可诊断运行态，避免 status 只能看到一个裸 pid。
func writeRuntimeState(state runtimeState) error {
	if state.PID <= 0 {
		return fmt.Errorf("runtime state missing pid")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path, err := resolveWeclawFile("weclaw.pid")
	if err != nil {
		return err
	}
	return securefile.Write(path, data)
}

// writeCurrentRuntimeState 记录当前前台服务进程，后台 daemon 子进程也走这里。
func writeCurrentRuntimeState(mode string) error {
	exe, _ := os.Executable()
	return writeRuntimeState(runtimeState{
		PID:       os.Getpid(),
		Exe:       exe,
		Version:   Version,
		Mode:      mode,
		StartedAt: time.Now(),
	})
}

func removeRuntimeState() error {
	path, err := resolveWeclawFile("weclaw.pid")
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func currentServiceMode() string {
	if os.Getenv(daemonChildEnv) == "1" {
		return "background"
	}
	return "foreground"
}

func readPid() (int, error) {
	state, err := readRuntimeState()
	if err != nil {
		return 0, err
	}
	return state.PID, nil
}
