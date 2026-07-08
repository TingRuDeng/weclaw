package cmd

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var (
	feishuUsersRevokeBotRef string
	feishuUsersRevokeAdmin  bool
)

var feishuUsersRevokeCmd = &cobra.Command{
	Use:   "revoke <union_id|user_id|open_id>",
	Short: "取消飞书用户授权",
	Args:  validateFeishuUsersRevokeArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsersRevoke(feishuUsersRevokeOptions{
			Selector: args[0],
			BotRef:   feishuUsersRevokeBotRef,
			Admin:    feishuUsersRevokeAdmin,
		})
	},
}

type feishuUsersRevokeOptions struct {
	Selector string
	BotRef   string
	Admin    bool
}

func init() {
	feishuUsersRevokeCmd.Flags().StringVar(&feishuUsersRevokeBotRef, "bot", "", "限定移除的飞书机器人 name 或 app_id")
	feishuUsersRevokeCmd.Flags().BoolVar(&feishuUsersRevokeAdmin, "admin", false, "同时从顶层 admin_users 移除")
	feishuUsersCmd.AddCommand(feishuUsersRevokeCmd)
}

// validateFeishuUsersRevokeArgs 校验取消授权命令必须带一个稳定用户 ID。
func validateFeishuUsersRevokeArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		return nil
	}
	return fmt.Errorf("用法: weclaw feishu users revoke <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]")
}

// runFeishuUsersRevoke 从飞书允许访问列表移除用户，可选同步移除管理员。
func runFeishuUsersRevoke(opts feishuUsersRevokeOptions) error {
	result, err := messaging.RevokeFeishuIdentity(messaging.FeishuIdentityRevokeRequest{
		Selector: opts.Selector,
		BotRef:   opts.BotRef,
		Admin:    opts.Admin,
	})
	if err != nil {
		return err
	}
	fmt.Println(messaging.RenderFeishuIdentityRevoke(result))
	return nil
}
