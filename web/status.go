package web

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/platform"
)

type platformStatus struct {
	Name               string `json:"name"`
	Enabled            bool   `json:"enabled"`
	CredentialsPresent bool   `json:"credentials_present"`
	AllowedUsersCount  int    `json:"allowed_users_count"`
}

type agentStatus struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
}

type statusView struct {
	DaemonRunning bool             `json:"daemon_running"`
	Platforms     []platformStatus `json:"platforms"`
	Agents        []agentStatus    `json:"agents"`
}

func (s *Server) buildStatus() statusView {
	cfg, err := s.cfg.load()
	if err != nil {
		return statusView{}
	}
	return statusView{
		DaemonRunning: daemonRunning(),
		Platforms:     platformStatuses(cfg),
		Agents:        agentStatuses(cfg),
	}
}

func platformStatuses(cfg *config.Config) []platformStatus {
	wechatCfg := cfg.Platforms[string(platform.PlatformWeChat)]
	wechatEnabled := wechatCfg.Enabled == nil || *wechatCfg.Enabled
	accounts, _ := ilink.LoadAllCredentials()

	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	feishuEnabled := feishuCfg.Enabled != nil && *feishuCfg.Enabled
	_, feishuErr := feishu.LoadCredentials()

	return []platformStatus{
		{
			Name:               string(platform.PlatformWeChat),
			Enabled:            wechatEnabled,
			CredentialsPresent: len(accounts) > 0,
			AllowedUsersCount:  len(wechatCfg.AllowedUsers),
		},
		{
			Name:               string(platform.PlatformFeishu),
			Enabled:            feishuEnabled,
			CredentialsPresent: feishuErr == nil,
			AllowedUsersCount:  len(feishuCfg.AllowedUsers),
		},
	}
}

func agentStatuses(cfg *config.Config) []agentStatus {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]agentStatus, 0, len(names))
	for _, name := range names {
		ag := cfg.Agents[name]
		command := ag.Command
		if ag.Type == "http" {
			command = ag.Endpoint
		}
		out = append(out, agentStatus{Name: name, Type: ag.Type, Command: command})
	}
	return out
}

// daemonRunning 通过 pid 文件 + 信号 0 探测守护进程是否存活。
func daemonRunning() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".weclaw", "weclaw.pid"))
	if err != nil {
		return false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
