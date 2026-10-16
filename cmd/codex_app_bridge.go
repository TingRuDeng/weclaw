package cmd

import (
	"fmt"
	"os"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

func init() {
	var restore bool
	appCommand := &cobra.Command{
		Use: "app", Short: "打开官方 Codex App；可恢复旧共享启动覆盖", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name, selected, err := configuredCodexAppServer(cfg)
			if err != nil {
				return err
			}
			runtime := agent.NewACPAgent(acpAgentConfigFromConfig(name, selected))
			if restore {
				if err := runtime.RestoreCodexAppLaunchEnvironment(command.Context()); err != nil {
					return err
				}
				_, err := fmt.Fprintln(command.OutOrStdout(), "已恢复仍匹配旧共享适配的 App 启动设置；下次正常打开 App 时生效，当前 App 未退出。")
				return err
			}
			if selected.EffectiveCodexHostMode() == "shared" {
				return runtime.PrepareCodexApp(command.Context())
			}
			return runtime.OpenNativeCodexApp(command.Context())
		},
	}
	appCommand.Flags().BoolVar(&restore, "restore", false, "恢复 WeClaw 旧共享桥接记录的 App 启动环境，不退出 App")
	codexCmd.AddCommand(appCommand)
	codexCmd.AddCommand(&cobra.Command{
		Use: "app-bridge", Hidden: true, DisableFlagParsing: true, SilenceUsage: true,
		RunE: func(command *cobra.Command, args []string) error {
			runtime, err := configuredCodexAppBridge()
			if err != nil {
				return err
			}
			return runtime.RunCodexAppBridge(command.Context(), args, os.Stdin, os.Stdout, os.Stderr)
		},
	})
}

func configuredCodexAppBridge() (*agent.ACPAgent, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	config.NormalizeCodexRemoteFirst(cfg)
	name, selected, err := configuredCodexAppServer(cfg)
	if err != nil {
		return nil, err
	}
	if mode := selected.EffectiveCodexHostMode(); mode != "auto" && mode != "shared" {
		return nil, fmt.Errorf("Codex App 共享入口需要 codex_host_mode auto 或 shared，当前为 %s", mode)
	}
	selected.CodexHostMode = "shared"
	if err := selected.ValidateCodexHostModeConfig(); err != nil {
		return nil, err
	}
	runtime := acpAgentConfigFromConfig(name, selected)
	return agent.NewACPAgent(runtime), nil
}
