package cmd

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var usersApproveAdmin bool

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "管理跨平台用户授权",
}

var usersApproveCodeCmd = &cobra.Command{
	Use:   "approve-code <授权码>",
	Short: "使用授权码批准用户访问",
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
			return nil
		}
		return fmt.Errorf("用法: weclaw users approve-code <授权码> [--admin]")
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := messaging.ApproveAccessCode(messaging.AccessCodeApprovalRequest{
			Code:  args[0],
			Admin: usersApproveAdmin,
		})
		if err != nil {
			return err
		}
		fmt.Printf("已授权 %s 用户: %s\n", result.Platform, result.Identity)
		if result.Admin {
			fmt.Println("已同步加入 admin_users。")
		}
		return nil
	},
}

func init() {
	usersApproveCodeCmd.Flags().BoolVar(&usersApproveAdmin, "admin", false, "同时写入顶层 admin_users")
	usersCmd.AddCommand(usersApproveCodeCmd)
	rootCmd.AddCommand(usersCmd)
}
