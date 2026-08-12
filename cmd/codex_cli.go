package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

var codexCLICmd = &cobra.Command{
	Use:                "cli [codex-options...]",
	Short:              "在共享 app-server 上启动 Codex CLI",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCodexCLI(cmd.Context(), args)
	},
}

var (
	loadCodexCLIConfig = config.Load
	codexCLIGetwd      = os.Getwd
	prepareCodexCLI    = func(ctx context.Context, cfg agent.ACPAgentConfig, opts agent.CodexCLILaunchOptions) (agent.CodexCLILaunch, error) {
		return agent.NewACPAgent(cfg).PrepareCodexCLILaunch(ctx, opts)
	}
	executeCodexCLI = func(ctx context.Context, launch agent.CodexCLILaunch) error {
		command := exec.CommandContext(ctx, launch.Command, launch.Args...)
		command.Dir = launch.Cwd
		command.Env = launch.Env
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	}
	prepareCodexCLIHostWithService = requestCodexCLIHostPreparation
)

const codexCLIHostPrepareTimeout = 30 * time.Second

var codexCLIHostHTTPClient = &http.Client{
	Timeout: codexCLIHostPrepareTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type codexCLIHostPrepareResponse struct {
	Status  string             `json:"status"`
	Host    agent.CodexCLIHost `json:"host"`
	Message string             `json:"message"`
}

func init() {
	codexCmd.AddCommand(codexCLICmd)
}

func runCodexCLI(ctx context.Context, args []string) error {
	frontendLease, err := agent.AcquireCodexCLIFrontendLease()
	if err != nil {
		return fmt.Errorf("无法启动受控 Codex CLI: %w", err)
	}
	defer frontendLease.Close()
	cfg, err := loadCodexCLIConfig()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	config.NormalizeCodexRemoteFirst(cfg)
	name, codexConfig, err := configuredCodexAppServer(cfg)
	if err != nil {
		return err
	}
	cwd, err := codexCLIGetwd()
	if err != nil {
		return fmt.Errorf("读取当前工作目录失败: %w", err)
	}
	allowHostStart, err := codexCLIHostStartAllowed()
	if err != nil {
		return err
	}
	var serviceHost agent.CodexCLIHost
	if !allowHostStart {
		serviceHost, err = prepareCodexCLIHostWithService(ctx, cfg)
		if err != nil {
			return fmt.Errorf("运行中的 WeClaw 无法准备受控 Codex CLI: %w", err)
		}
	}
	runtimeConfig := acpAgentConfigFromConfig(name, codexConfig)
	runtimeConfig.Cwd = cwd
	launch, err := prepareCodexCLI(ctx, runtimeConfig, agent.CodexCLILaunchOptions{
		Cwd: cwd, Args: append([]string(nil), args...), AllowHostStart: allowHostStart,
	})
	if err != nil {
		return err
	}
	if serviceHost.SocketPath != "" && filepath.Clean(serviceHost.SocketPath) != filepath.Clean(launch.SocketPath) {
		return fmt.Errorf("运行中的 WeClaw 与本地配置解析到不同 Codex Host，已拒绝连接")
	}
	if err := executeCodexCLI(ctx, launch); err != nil {
		return fmt.Errorf("Codex CLI 退出: %w", err)
	}
	return nil
}

func requestCodexCLIHostPreparation(ctx context.Context, cfg *config.Config) (agent.CodexCLIHost, error) {
	endpoint, err := runtimeAPIURL(cfg.APIAddr, "/api/codex/cli/prepare")
	if err != nil {
		return agent.CodexCLIHost{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return agent.CodexCLIHost{}, err
	}
	setRuntimeAPIToken(req, cfg.APIToken)
	resp, err := codexCLIHostHTTPClient.Do(req)
	if err != nil {
		return agent.CodexCLIHost{}, err
	}
	defer resp.Body.Close()
	var result codexCLIHostPrepareResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&result); err != nil {
		return agent.CodexCLIHost{}, fmt.Errorf("受控 CLI 准备响应无效: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return agent.CodexCLIHost{}, fmt.Errorf("受控 CLI 准备响应包含多余内容")
	}
	if resp.StatusCode != http.StatusOK || result.Status != "ok" {
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return agent.CodexCLIHost{}, fmt.Errorf("%s", message)
	}
	if strings.TrimSpace(result.Host.SocketPath) == "" {
		return agent.CodexCLIHost{}, fmt.Errorf("运行中的 WeClaw 未返回 Codex Host socket")
	}
	return result.Host, nil
}

// codexCLIHostStartAllowed delegates Host startup to a live WeClaw service. A
// stale runtime record does not block standalone daemon recovery.
func codexCLIHostStartAllowed() (bool, error) {
	path, err := resolveWeclawFile("weclaw.pid")
	if err != nil {
		return false, fmt.Errorf("无法定位 WeClaw 运行状态，已拒绝启动 Codex Host: %w", err)
	}
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("无法检查 WeClaw 运行状态，已拒绝启动 Codex Host: %w", err)
	}
	state, err := readRuntimeState()
	if err != nil {
		return false, fmt.Errorf("无法确认 WeClaw 运行状态，已拒绝启动 Codex Host: %w", err)
	}
	return !processExists(state.PID), nil
}
