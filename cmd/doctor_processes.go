package cmd

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type weclawProcess struct {
	PID  int
	Exe  string
	Args string
}

var doctorProcessesCmd = &cobra.Command{
	Use:   "processes",
	Short: "Inspect running weclaw processes",
	RunE: func(cmd *cobra.Command, args []string) error {
		processes, err := listWeclawProcesses()
		if err != nil {
			return err
		}
		result := summarizeWeclawProcesses(processes)
		fmt.Printf("%-7s %s%s\n", result.Status.symbol(), result.Name, detailSuffix(result.Detail))
		for _, proc := range processes {
			fmt.Printf("pid=%d path=%s args=%s\n", proc.PID, proc.Exe, proc.Args)
		}
		return nil
	},
}

func init() {
	doctorCmd.AddCommand(doctorProcessesCmd)
}

// listWeclawProcesses 只读取进程表，不主动结束任何进程。
func listWeclawProcesses() ([]weclawProcess, error) {
	out, err := exec.Command("ps", "-ax", "-o", "pid=", "-o", "command=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	return parseWeclawProcesses(out), nil
}

func parseWeclawProcesses(out []byte) []weclawProcess {
	lines := bytes.Split(out, []byte("\n"))
	processes := make([]weclawProcess, 0)
	for _, line := range lines {
		proc, ok := parseWeclawProcessLine(string(line))
		if ok {
			processes = append(processes, proc)
		}
	}
	return processes
}

// parseWeclawProcessLine 只识别 weclaw 自身的运行管理命令，避免误报 grep 或 shell。
func parseWeclawProcessLine(line string) (weclawProcess, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return weclawProcess{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return weclawProcess{}, false
	}
	exe := fields[1]
	if filepath.Base(exe) != "weclaw" {
		return weclawProcess{}, false
	}
	if !isWeclawRuntimeCommand(fields[2]) {
		return weclawProcess{}, false
	}
	return weclawProcess{PID: pid, Exe: exe, Args: strings.Join(fields[1:], " ")}, true
}

// isWeclawRuntimeCommand 限定与服务生命周期相关的命令。
func isWeclawRuntimeCommand(command string) bool {
	switch command {
	case "start", "restart", "status", "version":
		return true
	default:
		return false
	}
}

// summarizeWeclawProcesses 汇总残留进程和安装路径，供 doctor 输出稳定结论。
func summarizeWeclawProcesses(processes []weclawProcess) doctorResult {
	result := doctorResult{Name: "weclaw processes", Status: doctorOK}
	if len(processes) == 0 {
		result.Detail = "no weclaw process found"
		return result
	}
	paths := make(map[string]struct{})
	for _, proc := range processes {
		paths[proc.Exe] = struct{}{}
	}
	if len(processes) > 1 || len(paths) > 1 {
		result.Status = doctorWarn
	}
	result.Detail = fmt.Sprintf("%d process(es), %d install path(s): %s", len(processes), len(paths), joinSortedKeys(paths))
	return result
}

// joinSortedKeys 稳定输出安装路径，便于测试和排障比对。
func joinSortedKeys(values map[string]struct{}) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
