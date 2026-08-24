package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/spf13/cobra"
)

func init() {
	wechatCmd.AddCommand(loginCmd)
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
		if err := enableWeChatPlatform(); err != nil {
			return fmt.Errorf("保存微信平台启用状态失败: %w", err)
		}
		fmt.Printf("账号 %s 已添加。运行 weclaw start 启动服务。\n", creds.ILinkBotID)
		return nil
	},
}

func enableWeChatPlatform() error {
	return config.Update(func(cfg *config.Config) error {
		wechat := cfg.Platforms[string(platform.PlatformWeChat)]
		enabled := true
		wechat.Enabled = &enabled
		cfg.Platforms[string(platform.PlatformWeChat)] = wechat
		return nil
	})
}
