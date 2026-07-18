package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/spf13/cobra"
)

const (
	companionEndpointWaitTimeout   = 15 * time.Second
	companionEndpointRetryInterval = 250 * time.Millisecond
)

var (
	companionAgentFlag string
	companionCwdFlag   string
)

func init() {
	companionCmd.Flags().StringVar(&companionAgentFlag, "agent", "opencode", "要连接的 Agent 名称")
	companionCmd.Flags().StringVar(&companionCwdFlag, "cwd", "", "工作目录")
	rootCmd.AddCommand(companionCmd)
}

var companionCmd = &cobra.Command{
	Use:   "companion",
	Short: "启动本地可见 CLI 接管入口",
	RunE:  runCompanionCommand,
}

func runCompanionCommand(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cwd, err := resolveCompanionCwd(companionCwdFlag)
	if err != nil {
		return err
	}
	endpoint, err := waitForLiveCompanionEndpoint(ctx, companionAgentFlag, cwd, companionEndpointWaitOptions{})
	if err != nil {
		return err
	}
	runtime, err := createCompanionRuntime(endpoint)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return agent.RunCompanionClient(ctx, endpoint, runtime)
}

type companionRuntime interface {
	agent.CompanionRequestHandler
	Close() error
}

func createCompanionRuntime(endpoint agent.CompanionEndpoint) (companionRuntime, error) {
	switch strings.ToLower(endpoint.Agent) {
	case "opencode":
		return newOpenCodeCompanionRuntime(endpoint), nil
	case "codex":
		return nil, errors.New("Codex Companion 已停用：请通过 WeClaw 的单一共享 app-server 使用 Codex")
	default:
		return nil, fmt.Errorf("暂不支持 %s Companion，当前仅支持 opencode", endpoint.Agent)
	}
}

type companionEndpointWaitOptions struct {
	Timeout        time.Duration
	Interval       time.Duration
	ReadEndpoint   func(string, string) (agent.CompanionEndpoint, error)
	RemoveEndpoint func(string, string)
	Dial           func(context.Context, string, string) (net.Conn, error)
	Sleep          func(context.Context, time.Duration) error
}

func waitForLiveCompanionEndpoint(ctx context.Context, agentName string, cwd string, opts companionEndpointWaitOptions) (agent.CompanionEndpoint, error) {
	opts = fillCompanionEndpointWaitOptions(opts)
	waitCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var lastErr error
	for {
		endpoint, err := opts.ReadEndpoint(agentName, cwd)
		if err == nil {
			if liveErr := ensureCompanionEndpointLive(waitCtx, endpoint, opts.Dial); liveErr == nil {
				return endpoint, nil
			} else {
				lastErr = liveErr
				// 端点文件可能来自已退出的 companion；删除后继续等待新进程重新登记。
				opts.RemoveEndpoint(agentName, cwd)
			}
		} else {
			lastErr = err
		}

		if err := opts.Sleep(waitCtx, opts.Interval); err != nil {
			return agent.CompanionEndpoint{}, fmt.Errorf("等待 Companion 入口就绪超时: agent=%s cwd=%s；最后一次错误: %w", agentName, cwd, lastErr)
		}
	}
}

func fillCompanionEndpointWaitOptions(opts companionEndpointWaitOptions) companionEndpointWaitOptions {
	if opts.Timeout <= 0 {
		opts.Timeout = companionEndpointWaitTimeout
	}
	if opts.Interval <= 0 {
		opts.Interval = companionEndpointRetryInterval
	}
	if opts.ReadEndpoint == nil {
		opts.ReadEndpoint = agent.ReadCompanionEndpoint
	}
	if opts.RemoveEndpoint == nil {
		opts.RemoveEndpoint = agent.RemoveCompanionEndpoint
	}
	if opts.Dial == nil {
		dialer := &net.Dialer{}
		opts.Dial = dialer.DialContext
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}
	return opts
}

func ensureCompanionEndpointLive(ctx context.Context, endpoint agent.CompanionEndpoint, dial func(context.Context, string, string) (net.Conn, error)) error {
	conn, err := dial(ctx, "tcp", endpoint.Address())
	if err != nil {
		return err
	}
	return conn.Close()
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resolveCompanionCwd(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "."
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("解析工作目录失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("工作目录不存在: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("工作目录不是目录: %s", abs)
	}
	return abs, nil
}
