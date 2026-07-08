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
	Short: "查看飞书自动发现用户",
}

var (
	feishuUsersApproveBotRef string
	feishuUsersApproveAdmin  bool
)

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

var feishuUsersApproveCmd = &cobra.Command{
	Use:   "approve <union_id|user_id|open_id>",
	Short: "确认飞书用户并写入配置",
	Args:  validateFeishuUsersApproveArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeishuUsersApprove(feishuUsersApproveOptions{
			Selector: args[0],
			BotRef:   feishuUsersApproveBotRef,
			Admin:    feishuUsersApproveAdmin,
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
	feishuUsersCmd.AddCommand(feishuUsersPendingCmd, feishuUsersListCmd, feishuUsersApproveCmd)
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
	botLabels, lookupAccounts := feishuBotUserListMetadata()
	nameLookup := lookupFeishuIdentityNames(context.Background(), views, lookupAccounts)
	printFeishuIdentityViews(title, views, botLabels, nameLookup)
	return nil
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
func printFeishuIdentityViews(title string, views []messaging.FeishuIdentityView, botLabels map[string]string, names feishuIdentityNameLookupResult) {
	if len(views) == 0 {
		fmt.Printf("%s: 暂无\n", title)
		return
	}
	fmt.Println(title + ":")
	printFeishuIdentityNameWarnings(names.Warnings)
	for i, view := range views {
		printFeishuIdentityView(i+1, view, botLabels, names.Names)
	}
}

// printFeishuIdentityView 输出单条身份详情和可复制的授权命令。
func printFeishuIdentityView(index int, view messaging.FeishuIdentityView, botLabels map[string]string, names map[string]string) {
	fmt.Printf("%d. %s\n", index, feishuIdentityDisplayLabel(view, names))
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
		fmt.Printf("   机器人: %s\n", strings.Join(feishuBotLabelsForAccounts(view.Accounts, botLabels), ", "))
	}
	fmt.Printf("   状态: %s\n", feishuIdentityViewStatus(view))
	printFeishuIdentityApproveHints(view)
}

// printFeishuIdentityNameWarnings 把可恢复的通讯录查询失败展示给用户。
func printFeishuIdentityNameWarnings(warnings []string) {
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning != "" {
			fmt.Printf("姓名查询失败: %s\n", warning)
		}
	}
}

// feishuIdentityDisplayLabel 优先显示通讯录姓名，同时保留稳定 ID 便于复制授权。
func feishuIdentityDisplayLabel(view messaging.FeishuIdentityView, names map[string]string) string {
	name := feishuIdentityResolvedName(view, names)
	if name == "" || name == view.Key {
		return view.Key
	}
	return fmt.Sprintf("%s (%s)", name, view.Key)
}

// feishuIdentityViewStatus 把身份授权状态转为面向用户的中文文案。
func feishuIdentityViewStatus(view messaging.FeishuIdentityView) string {
	if view.Approved {
		return "已授权"
	}
	if view.Pending {
		return "待确认"
	}
	return "已发现"
}

// feishuBotAccountLabels 返回 app_id 到可读机器人名称的映射。
func feishuBotAccountLabels() map[string]string {
	labels, _ := feishuBotUserListMetadata()
	return labels
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
	fmt.Printf("   授权访问: weclaw feishu users approve %s\n", selector)
	if strings.TrimSpace(view.UnionID) != "" {
		fmt.Printf("   设为管理员: weclaw feishu users approve %s --admin\n", view.UnionID)
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
