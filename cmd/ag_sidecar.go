package cmd

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
)

var agSidecarSocket string

var agSidecarCmd = &cobra.Command{
	Use:   "ag-sidecar",
	Short: "运行 Antigravity Sidecar",
	Long:  "运行 Antigravity Sidecar，把官方 ConversationService 和 agentapi 暴露为本机私有 Unix socket。",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runAgSidecar(cmd)
	},
}

func init() {
	agSidecarCmd.Flags().StringVar(&agSidecarSocket, "socket", "", "覆盖私有 Unix socket 路径")
	rootCmd.AddCommand(agSidecarCmd)
}

func runAgSidecar(cmd *cobra.Command) error {
	if strings.TrimSpace(os.Getenv("ANTIGRAVITY_LS_ADDRESS")) == "" {
		return fmt.Errorf("启动 Antigravity Sidecar 前必须设置 ANTIGRAVITY_LS_ADDRESS")
	}
	if strings.TrimSpace(os.Getenv("ANTIGRAVITY_CSRF_TOKEN")) == "" {
		return fmt.Errorf("启动 Antigravity Sidecar 前必须设置 ANTIGRAVITY_CSRF_TOKEN")
	}
	service, err := antigravity.NewConversationServiceFromEnv()
	if err != nil {
		return err
	}
	appDataDir := antigravity.DefaultAppDataDir()
	runner, err := antigravity.NewAgentRunner(appDataDir)
	if err != nil {
		log.Printf("[antigravity] agentapi unavailable; message sending stays disabled: %v", err)
	}
	server, err := antigravity.NewServer(antigravity.ServerOptions{
		SocketPath: agSidecarSocket, AppDataDir: appDataDir, ConversationService: service, Runner: runner,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("[antigravity] sidecar listening on %s", server.SocketPath())
	return server.Run(ctx)
}
