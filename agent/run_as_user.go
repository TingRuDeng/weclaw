package agent

import (
	"os/user"
	"strings"
)

// runAsUserSpec 描述以其他 Unix 用户运行 agent 的隔离配置。
type runAsUserSpec struct {
	User       string   // 目标 Unix 用户名；空表示不隔离
	PreserveEnv []string // 通过 sudo --preserve-env 透传的环境变量名白名单
}

// shouldIsolate 判断是否需要切换用户：目标用户非空且不是当前用户。
func (s runAsUserSpec) shouldIsolate() bool {
	target := strings.TrimSpace(s.User)
	if target == "" {
		return false
	}
	if current, err := user.Current(); err == nil {
		if strings.EqualFold(strings.TrimSpace(current.Username), target) {
			return false
		}
	}
	return true
}

// wrapCommand 在需要隔离时把命令包装为 `sudo -n -u <user> [--preserve-env=...] command args...`。
// -n 表示非交互（不能等待密码输入），要求目标已配置免密 sudo。未隔离时原样返回。
func (s runAsUserSpec) wrapCommand(command string, args []string) (string, []string) {
	if !s.shouldIsolate() {
		return command, args
	}
	wrapped := []string{"-n", "-u", strings.TrimSpace(s.User)}
	if env := cleanPreserveEnv(s.PreserveEnv); env != "" {
		wrapped = append(wrapped, "--preserve-env="+env)
	}
	wrapped = append(wrapped, command)
	wrapped = append(wrapped, args...)
	return "sudo", wrapped
}

// cleanPreserveEnv 规整透传环境变量名，去除空白与非法（含 =）项。
func cleanPreserveEnv(names []string) string {
	cleaned := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "=") || seen[name] {
			continue
		}
		seen[name] = true
		cleaned = append(cleaned, name)
	}
	return strings.Join(cleaned, ",")
}
