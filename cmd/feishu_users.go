package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var feishuUsersCmd = &cobra.Command{
	Use:   "users",
	Short: "管理飞书用户授权",
}

var (
	feishuUsersApproveBotRef string
	feishuUsersApproveAdmin  bool
	feishuUsersApproveName   string
)

var feishuUsersPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "查看待授权飞书用户",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsers("pending")
	},
}

var feishuUsersListCmd = &cobra.Command{
	Use:   "list",
	Short: "查看已授权飞书用户",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsers("list")
	},
}

var feishuUsersApproveCmd = &cobra.Command{
	Use:   "approve <union_id|user_id|open_id>",
	Short: "授权飞书用户访问机器人",
	Args:  validateFeishuUsersApproveArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsersApprove(feishuUsersApproveOptions{
			Selector: args[0],
			BotRef:   feishuUsersApproveBotRef,
			Admin:    feishuUsersApproveAdmin,
		})
	},
}

var feishuUsersRenameCmd = &cobra.Command{
	Use:   "rename <union_id|user_id|open_id> <显示名>",
	Short: "备注飞书用户姓名",
	Args:  validateFeishuUsersRenameArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsersRename(feishuUsersRenameOptions{
			Selector:    args[0],
			DisplayName: args[1],
		})
	},
}

var feishuUsersApproveCodeCmd = &cobra.Command{
	Use:   "approve-code <授权码>",
	Short: "使用授权码授权飞书用户",
	Args:  validateFeishuUsersApproveCodeArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsersApproveCode(feishuUsersApproveCodeOptions{
			Code:        args[0],
			BotRef:      feishuUsersApproveBotRef,
			Admin:       feishuUsersApproveAdmin,
			DisplayName: feishuUsersApproveName,
		})
	},
}

type feishuUsersApproveOptions struct {
	Selector string
	BotRef   string
	Admin    bool
}

func init() {
	feishuUsersApproveCmd.Flags().StringVar(&feishuUsersApproveBotRef, "bot", "", "限定写入的飞书机器人 name 或 app_id")
	feishuUsersApproveCmd.Flags().BoolVar(&feishuUsersApproveAdmin, "admin", false, "同时写入顶层 admin_users")
	feishuUsersApproveCodeCmd.Flags().StringVar(&feishuUsersApproveBotRef, "bot", "", "限定写入的飞书机器人 name 或 app_id")
	feishuUsersApproveCodeCmd.Flags().BoolVar(&feishuUsersApproveAdmin, "admin", false, "同时写入顶层 admin_users")
	feishuUsersApproveCodeCmd.Flags().StringVar(&feishuUsersApproveName, "name", "", "为该用户写入本地显示名")
	feishuUsersCmd.AddCommand(feishuUsersPendingCmd, feishuUsersListCmd, feishuUsersApproveCmd, feishuUsersApproveCodeCmd, feishuUsersRenameCmd)
}

func runFeishuUsers(kind string) error {
	pendingOnly := kind == "pending"
	views, err := loadFeishuUserViews(kind, pendingOnly)
	if err != nil {
		return err
	}
	title := "已授权飞书用户"
	if pendingOnly {
		title = "待确认飞书用户"
	}
	botLabels, lookupAccounts := feishuBotUserListMetadata()
	nameLookup := lookupFeishuIdentityNamesForViews(context.Background(), views, lookupAccounts)
	printFeishuIdentityViews(title, views, botLabels, nameLookup, pendingOnly)
	return nil
}

func loadFeishuUserViews(kind string, pendingOnly bool) ([]messaging.FeishuIdentityView, error) {
	if kind == "list" {
		return messaging.LoadApprovedFeishuIdentityViews("")
	}
	return messaging.LoadFeishuIdentityViews("", pendingOnly)
}

// validateFeishuUsersApproveArgs 校验授权命令必须带一个可识别用户 ID。
func validateFeishuUsersApproveArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		return nil
	}
	return fmt.Errorf("用法: weclaw feishu users approve <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]")
}

// runFeishuUsersApprove 将已发现身份写入飞书允许访问列表，可选同步管理员。
func runFeishuUsersApprove(opts feishuUsersApproveOptions) error {
	result, err := messaging.ApproveFeishuIdentity(messaging.FeishuIdentityApproveRequest{
		Selector: opts.Selector,
		BotRef:   opts.BotRef,
		Admin:    opts.Admin,
	})
	if err != nil {
		return err
	}
	fmt.Println(messaging.RenderFeishuIdentityApproval(result))
	return nil
}

// printFeishuIdentityViews 输出身份列表，并在联系人查询失败时显式提示原因。
func printFeishuIdentityViews(title string, views []messaging.FeishuIdentityView, botLabels map[string]string, names feishuIdentityNameLookupResult, showApprovalCode bool) {
	if len(views) == 0 {
		fmt.Printf("%s: 暂无\n", title)
		return
	}
	fmt.Println(title + ":")
	printFeishuIdentityNameWarnings(names.Warnings)
	for i, view := range views {
		printFeishuIdentityView(i+1, view, botLabels, names.Names, showApprovalCode)
	}
}

// printFeishuIdentityView 输出单条身份详情和可复制的授权命令。
func printFeishuIdentityView(index int, view messaging.FeishuIdentityView, botLabels map[string]string, names map[string]string, showApprovalCode bool) {
	fmt.Printf("%d. %s\n", index, feishuIdentityDisplayLabel(view, names))
	if !showApprovalCode && len(view.AuthorizedAccounts) > 0 {
		fmt.Printf("   已授权机器人: %s\n", strings.Join(feishuBotLabelsForAccounts(view.AuthorizedAccounts, botLabels), ", "))
		fmt.Printf("   用户类型: %s\n", feishuIdentityUserType(view))
	}
	if showApprovalCode && len(view.UnauthorizedAccounts) > 0 {
		fmt.Printf("   待授权机器人: %s\n", strings.Join(feishuBotLabelsForAccounts(view.UnauthorizedAccounts, botLabels), ", "))
	}
	if len(view.AuthorizedAccounts) == 0 && len(view.UnauthorizedAccounts) == 0 && len(view.Accounts) > 0 {
		fmt.Printf("   相关机器人: %s\n", strings.Join(feishuBotLabelsForAccounts(view.Accounts, botLabels), ", "))
	}
	if showApprovalCode {
		fmt.Printf("   状态: %s\n", feishuIdentityViewStatus(view, showApprovalCode))
	}
	if showApprovalCode && strings.TrimSpace(view.AuthCode) != "" {
		fmt.Printf("   授权码: %s\n", view.AuthCode)
		fmt.Printf("   授权命令: weclaw feishu users approve-code %s\n", view.AuthCode)
		if strings.TrimSpace(view.UnionID) != "" {
			fmt.Printf("   授权并设为管理员: weclaw feishu users approve-code %s --admin\n", view.AuthCode)
		}
		return
	}
	if showApprovalCode && len(view.UnauthorizedAccounts) > 0 {
		printFeishuIdentityApproveHints(view)
	}
}

func feishuIdentityUserType(view messaging.FeishuIdentityView) string {
	if view.Admin {
		return "管理员"
	}
	return "普通用户"
}

// feishuIdentityViewStatus 把身份授权状态转为面向用户的中文文案。
func feishuIdentityViewStatus(view messaging.FeishuIdentityView, showApprovalCode bool) string {
	if showApprovalCode && strings.TrimSpace(view.AuthCode) != "" {
		return "待确认"
	}
	if showApprovalCode && len(view.UnauthorizedAccounts) > 0 {
		return "待授权"
	}
	if len(view.AuthorizedAccounts) > 0 || view.Approved {
		return "已授权"
	}
	if view.Pending {
		return "待确认"
	}
	return "已发现"
}

// feishuBotUserListMetadata 同时生成机器人展示标签和联系人查询所需账号信息。
func feishuBotUserListMetadata() (map[string]string, []feishuIdentityNameLookupAccount) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil
	}
	labels := make(map[string]string)
	accounts := make([]feishuIdentityNameLookupAccount, 0, len(cfg.Platforms["feishu"].Bots))
	for _, bot := range cfg.Platforms["feishu"].Bots {
		appID := strings.TrimSpace(bot.AppID)
		if appID == "" {
			continue
		}
		label := feishuBotAccountLabel(bot)
		labels[appID] = label
		accounts = append(accounts, feishuIdentityNameLookupAccount{
			Name:  strings.TrimSpace(bot.Name),
			AppID: appID,
			Label: label,
		})
	}
	return labels, accounts
}

// feishuBotAccountLabel 生成能区分同名机器人的展示标签。
func feishuBotAccountLabel(bot config.FeishuBotConfig) string {
	display := config.FeishuBotDisplayName(bot)
	name := strings.TrimSpace(bot.Name)
	appID := strings.TrimSpace(bot.AppID)
	if display != "" && display != name {
		return fmt.Sprintf("%s (%s, %s)", display, name, appID)
	}
	if name != "" {
		return fmt.Sprintf("%s (%s)", name, appID)
	}
	return appID
}

// feishuBotLabelsForAccounts 把身份记录中的 app_id 列表渲染为用户可读标签。
func feishuBotLabelsForAccounts(accounts []string, labels map[string]string) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		account = strings.TrimSpace(account)
		if account == "" {
			continue
		}
		if label := labels[account]; label != "" {
			out = append(out, label)
			continue
		}
		out = append(out, account)
	}
	return out
}

// printFeishuIdentityApproveHints 输出访问授权和管理员授权的下一步命令。
func printFeishuIdentityApproveHints(view messaging.FeishuIdentityView) {
	selector := firstFeishuIdentitySelector(view)
	if selector == "" {
		return
	}
	fmt.Printf("   授权命令: weclaw feishu users approve %s\n", selector)
	fmt.Println("   授权说明: 执行上面的授权命令可授权该用户访问待授权机器人。")
	if strings.TrimSpace(view.UnionID) != "" {
		fmt.Printf("   管理员命令: weclaw feishu users approve %s --admin\n", view.UnionID)
		fmt.Println("   管理员说明: 需要同时设为管理员时执行上面的管理员命令。")
	}
}

// firstFeishuIdentitySelector 选择授权命令最稳定的用户标识。
func firstFeishuIdentitySelector(view messaging.FeishuIdentityView) string {
	for _, value := range []string{view.UnionID, view.UserID, view.OpenID, view.Key} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
