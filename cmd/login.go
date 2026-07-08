package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "扫码添加微信账号",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		creds, err := doLogin(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("账号 %s 已添加。运行 weclaw start 启动服务。\n", creds.ILinkBotID)
		return nil
	},
}
