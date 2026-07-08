package cmd

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var feishuUsersCmd = &cobra.Command{
	Use:   "users",
	Short: "查看飞书自动发现用户",
}

var feishuUsersPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "查看待确认飞书用户",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsers("pending")
	},
}

var feishuUsersListCmd = &cobra.Command{
	Use:   "list",
	Short: "查看全部飞书用户身份",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsers("list")
	},
}

func init() {
	feishuUsersCmd.AddCommand(feishuUsersPendingCmd, feishuUsersListCmd)
}

func runFeishuUsers(kind string) error {
	pendingOnly := kind == "pending"
	views, err := messaging.LoadFeishuIdentityViews("", pendingOnly)
	if err != nil {
		return err
	}
	title := "飞书用户身份"
	if pendingOnly {
		title = "待确认飞书用户"
	}
	printFeishuIdentityViews(title, views)
	return nil
}

func printFeishuIdentityViews(title string, views []messaging.FeishuIdentityView) {
	if len(views) == 0 {
		fmt.Printf("%s: 暂无\n", title)
		return
	}
	fmt.Println(title + ":")
	for i, view := range views {
		printFeishuIdentityView(i+1, view)
	}
}

func printFeishuIdentityView(index int, view messaging.FeishuIdentityView) {
	fmt.Printf("%d. %s\n", index, view.Key)
	if view.UnionID != "" {
		fmt.Printf("   union_id: %s\n", view.UnionID)
	}
	if view.UserID != "" {
		fmt.Printf("   user_id: %s\n", view.UserID)
	}
	if view.OpenID != "" {
		fmt.Printf("   open_id: %s\n", view.OpenID)
	}
	if len(view.Accounts) > 0 {
		fmt.Printf("   机器人: %s\n", strings.Join(view.Accounts, ", "))
	}
	fmt.Printf("   状态: %s\n", feishuIdentityViewStatus(view))
}

func feishuIdentityViewStatus(view messaging.FeishuIdentityView) string {
	if view.Approved {
		return "已授权"
	}
	if view.Pending {
		return "待确认"
	}
	return "已发现"
}
