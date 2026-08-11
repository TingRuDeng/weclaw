package cmd

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var (
	feishuUsersRevokeBotRef string
)

var feishuUsersRevokeCmd = &cobra.Command{
	Use:   "revoke <union_id|user_id|open_id>",
	Short: "取消飞书用户授权",
	Args:  validateFeishuUsersRevokeArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsersRevoke(feishuUsersRevokeOptions{
			Selector: args[0],
			BotRef:   feishuUsersRevokeBotRef,
		})
	},
}

type feishuUsersRevokeOptions struct {
	Selector string
	BotRef   string
}

func init() {
	feishuUsersRevokeCmd.Flags().StringVar(&feishuUsersRevokeBotRef, "bot", "", "限定移除的飞书机器人 name 或 app_id")
	feishuUsersCmd.AddCommand(feishuUsersRevokeCmd)
}

// validateFeishuUsersRevokeArgs 校验取消授权命令必须带一个稳定用户 ID。
func validateFeishuUsersRevokeArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		return nil
	}
	return fmt.Errorf("用法: weclaw feishu users revoke <union_id|user_id|open_id> [--bot <name|app_id>]")
}

// runFeishuUsersRevoke 从飞书允许访问列表移除用户。
func runFeishuUsersRevoke(opts feishuUsersRevokeOptions) error {
	result, err := messaging.RevokeFeishuIdentity(messaging.FeishuIdentityRevokeRequest{
		Selector: opts.Selector,
		BotRef:   opts.BotRef,
	})
	if err != nil {
		return err
	}
	fmt.Println(messaging.RenderFeishuIdentityRevoke(result))
	return nil
}
