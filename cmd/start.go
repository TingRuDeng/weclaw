package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

const daemonClaudePreflightEnv = "WECLAW_DAEMON_CLAUDE_PREFLIGHT"

var (
	foregroundFlag bool
	apiAddrFlag    string
)

// init 注册 start 命令及其前台运行、API 地址参数。
func init() {
	startCmd.Flags().BoolVarP(&foregroundFlag, "foreground", "f", false, "前台运行，默认后台运行")
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "HTTP API 监听地址，默认 127.0.0.1:18011")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "启动消息服务",
	RunE:  runStart,
}

type preparedStart struct {
	cfg *config.Config
	run func() error
}

type startPreparationOps struct {
	loadConfig func() (*config.Config, error)
	preflight  func(context.Context, *config.Config) error
	start      func(*config.Config) error
}

// runStart 加载配置后按前台或后台模式进入对应启动流程。
func runStart(cmd *cobra.Command, args []string) error {
	daemonLog, err := configureDaemonLogging()
	if err != nil {
		return err
	}
	if daemonLog != nil {
		defer daemonLog.Close()
	}
	start := runBackgroundStart
	if foregroundFlag {
		start = runForegroundStart
	}
	prepared, err := prepareConfiguredStart(cmd.Context(), start)
	if err != nil {
		return err
	}
	return prepared.run()
}

// loadStartConfig 加载启动配置，并统一包装配置文件错误。
func loadStartConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	return cfg, nil
}

// prepareConfiguredStart 使用正式依赖生成可延迟执行的已预检启动闭包。
func prepareConfiguredStart(ctx context.Context, start func(*config.Config) error) (preparedStart, error) {
	return prepareStart(ctx, startPreparationOps{
		loadConfig: loadStartConfig,
		preflight:  configuredStartPreflight(),
		start:      start,
	})
}

// configuredStartPreflight 让后台子进程复用父进程已完成的能力握手。
func configuredStartPreflight() func(context.Context, *config.Config) error {
	if os.Getenv(daemonChildEnv) == "1" {
		return preflightDaemonChildConfig
	}
	return preflightStartConfig
}

// preflightDaemonChildConfig 只复核配置与命令路径，避免在获取运行锁前重复启动 ACP。
func preflightDaemonChildConfig(_ context.Context, cfg *config.Config) error {
	if err := preflightCodexMultiFrontend(cfg); err != nil {
		return err
	}
	if err := cfg.PreflightClaudeACPAgents(config.ClaudeACPPreflightOptions{
		LookPath: config.LookPath,
		Probe:    func(string, config.AgentConfig) error { return nil },
	}); err != nil {
		return err
	}
	expected := strings.TrimSpace(os.Getenv(daemonClaudePreflightEnv))
	if expected == "" {
		return fmt.Errorf("后台子进程缺少父进程 Claude 预检指纹，请重新执行 weclaw start")
	}
	actual, err := claudePreflightFingerprint(cfg)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("Claude 配置在父进程预检后发生变化，请重新执行 weclaw start")
	}
	return nil
}

func claudePreflightFingerprint(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("Claude 预检配置为空")
	}
	agentCfg, present := cfg.Agents["claude"]
	payload, err := json.Marshal(struct {
		Present bool               `json:"present"`
		Agent   config.AgentConfig `json:"agent"`
	}{Present: present, Agent: agentCfg})
	if err != nil {
		return "", fmt.Errorf("生成 Claude 预检指纹: %w", err)
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

// prepareStart 固化一次配置快照，避免停止服务前后重复加载配置。
func prepareStart(ctx context.Context, ops startPreparationOps) (preparedStart, error) {
	cfg, err := ops.loadConfig()
	if err != nil {
		return preparedStart{}, err
	}
	if err := ops.preflight(ctx, cfg); err != nil {
		return preparedStart{}, fmt.Errorf("启动预检失败: %w", err)
	}
	return preparedStart{cfg: cfg, run: func() error { return ops.start(cfg) }}, nil
}

// preflightStartConfig 验证 Claude ACP adapter 可执行且具备会话列表与恢复能力。
func preflightStartConfig(ctx context.Context, cfg *config.Config) error {
	modified := config.DetectAndConfigure(cfg)
	if err := preflightCodexMultiFrontend(cfg); err != nil {
		return err
	}
	err := cfg.PreflightClaudeACPAgents(config.ClaudeACPPreflightOptions{
		LookPath: config.LookPath,
		Probe: func(name string, agentCfg config.AgentConfig) error {
			return defaultClaudeACPProbe(ctx, name, agentCfg)
		},
	})
	if err != nil || !modified {
		return err
	}
	return persistDetectedStartConfig(modified, cfg, config.Update)
}

func preflightCodexMultiFrontend(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	agentCfg, ok := cfg.Agents["codex"]
	if !ok || !agentCfg.EffectiveCodexMultiFrontend() {
		return nil
	}
	result := checkCodexStandalone(cfg, doctorDeps{codexHome: defaultDoctorCodexHome})
	if result.Status == doctorFail {
		return fmt.Errorf("%s", result.Detail)
	}
	return nil
}

// persistDetectedStartConfig 确保后台子进程能重新加载同一份预检配置。
func persistDetectedStartConfig(modified bool, cfg *config.Config, update func(func(*config.Config) error) error) error {
	if !modified {
		return nil
	}
	var committed *config.Config
	if err := update(func(latest *config.Config) error {
		config.DetectAndConfigure(latest)
		if cfg != nil {
			if _, err := applyPreflightedClaudeConfig(cfg, latest); err != nil {
				return err
			}
		}
		committed = latest
		return nil
	}); err != nil {
		return fmt.Errorf("保存自动探测配置失败: %w", err)
	}
	if cfg != nil && committed != nil {
		*cfg = *committed
	}
	return nil
}

// applyPreflightedClaudeConfig 只接受与已完成 ACP 能力握手完全相同的 Claude 配置。
// command 可从别名解析为同一绝对路径；其它字段变化必须重新执行一次 start 预检。
func applyPreflightedClaudeConfig(preflighted *config.Config, latest *config.Config) (bool, error) {
	if preflighted == nil || latest == nil {
		return false, nil
	}
	want, wantOK := preflighted.Agents["claude"]
	got, gotOK := latest.Agents["claude"]
	if !wantOK || !gotOK {
		if wantOK == gotOK {
			return false, nil
		}
		return false, fmt.Errorf("Claude 配置在启动预检后发生变化，请重新执行 weclaw start")
	}
	wantCommand, err := config.LookPath(want.Command)
	if err != nil {
		return false, fmt.Errorf("重新确认已预检 Claude 命令 %q: %w", want.Command, err)
	}
	gotCommand, err := config.LookPath(got.Command)
	if err != nil {
		return false, fmt.Errorf("重新确认当前 Claude 命令 %q: %w", got.Command, err)
	}
	want.Command = wantCommand
	resolvedGot := got
	resolvedGot.Command = gotCommand
	if !reflect.DeepEqual(want, resolvedGot) {
		return false, fmt.Errorf("Claude 配置在启动预检后发生变化，请重新执行 weclaw start")
	}
	changed := !reflect.DeepEqual(got, want)
	latest.Agents["claude"] = want
	return changed, nil
}
