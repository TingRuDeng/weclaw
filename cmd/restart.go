package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var restartForceFlag bool

func init() {
	restartCmd.Flags().BoolVar(&restartForceFlag, "force", false, "即使有运行中任务也强制重启")
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "重启后台 WeClaw 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureConfiguredRestartSafe(context.Background(), restartForceFlag); err != nil {
			return err
		}
		fmt.Println("正在停止 WeClaw...")
		if err := stopAllWeclaw(); err != nil {
			return err
		}
		fmt.Println("正在启动 WeClaw...")
		return runDaemon()
	},
}
