package cmd

import (
	"fmt"
	"os"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

func init() {
	codexCmd.AddCommand(&cobra.Command{
		Use: "app", Short: "打开使用共享会话服务的 Codex App", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runtime, err := configuredCodexAppBridge()
			if err != nil {
				return err
			}
			return runtime.PrepareCodexApp(command.Context())
		},
	})
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
