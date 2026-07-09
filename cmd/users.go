package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var usersApproveAdmin bool

var wechatCmd = &cobra.Command{
	Use:   "wechat",
	Short: "管理微信平台",
}

func init() {
	wechatCmd.AddCommand(newWechatUsersCmd())
	rootCmd.AddCommand(wechatCmd)
}

func newWechatUsersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "管理微信用户授权",
	}
	cmd.AddCommand(newWechatUsersListCmd(), newWechatUsersPendingCmd(), newWechatUsersApproveCodeCmd("weclaw wechat users"))
	return cmd
}

func newWechatUsersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "查看已授权微信用户",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUsersList()
		},
	}
}

func newWechatUsersPendingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pending",
		Short: "查看待授权微信用户",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUsersPending()
		},
	}
}

func newWechatUsersApproveCodeCmd(commandPath string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve-code <授权码>",
		Short: "使用授权码授权微信用户",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				return nil
			}
			return fmt.Errorf("用法: %s approve-code <授权码> [--admin]", commandPath)
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
	cmd.Flags().BoolVar(&usersApproveAdmin, "admin", false, "同时写入顶层 admin_users")
	return cmd
}

// runUsersList 输出微信 allowed_users，便于管理员直接确认当前授权状态。
func runUsersList() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	users := cfg.Platforms["wechat"].AllowedUsers
	if len(users) == 0 {
		fmt.Println("已授权微信用户: 暂无")
		return nil
	}
	fmt.Println("已授权微信用户:")
	for i, user := range users {
		fmt.Printf("%d. %s\n", i+1, user)
		if usersIsAdmin(cfg.AdminUsers, user) {
			fmt.Println("   用户类型: 管理员")
		} else {
			fmt.Println("   用户类型: 普通用户")
		}
	}
	return nil
}

// runUsersPending 输出待授权微信授权码，并给出可复制的批准命令。
func runUsersPending() error {
	views := messaging.LoadPendingAccessCodeViews("")
	sort.Slice(views, func(i int, j int) bool {
		return views[i].Code < views[j].Code
	})
	count := 0
	for _, view := range views {
		if view.Platform != "wechat" {
			continue
		}
		count++
		if count == 1 {
			fmt.Println("待授权微信用户:")
		}
		fmt.Printf("%d. %s\n", count, view.UserID)
		if strings.TrimSpace(view.AccountID) != "" {
			fmt.Printf("   微信账号: %s\n", view.AccountID)
		}
		fmt.Printf("   授权码: %s\n", view.Code)
		fmt.Printf("   过期时间: %s\n", view.ExpiresAt)
		fmt.Printf("   授权命令: weclaw wechat users approve-code %s\n", view.Code)
		fmt.Printf("   管理员命令: weclaw wechat users approve-code %s --admin\n", view.Code)
	}
	if count == 0 {
		fmt.Println("待授权微信用户: 暂无")
	}
	return nil
}

func usersIsAdmin(adminUsers []string, user string) bool {
	for _, admin := range adminUsers {
		if admin == user {
			return true
		}
	}
	return false
}
