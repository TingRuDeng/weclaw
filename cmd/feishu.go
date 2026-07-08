package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/spf13/cobra"
)

var (
	feishuBotName                 string
	feishuLoginAppID              string
	feishuLoginAppSecret          string
	feishuBotDisplayName          string
	feishuBotAliases              string
	feishuBootstrapAllowedUsers   string
	feishuBootstrapDefaultAgent   string
	feishuBootstrapProgressMode   string
	feishuBootstrapRequireMention bool
	validateFeishuCreds           = feishu.ValidateCredentials
	saveFeishuCreds               = feishu.SaveCredentialsForBot
)

func init() {
	feishuLoginCmd.Flags().StringVar(&feishuBotName, "name", "", "飞书机器人内部 ID")
	feishuLoginCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "飞书 app_id")
	feishuLoginCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "飞书 app_secret")
	feishuStatusCmd.Flags().StringVar(&feishuBotName, "name", "", "飞书机器人内部 ID")
	feishuBootstrapCmd.Flags().StringVar(&feishuBotName, "name", "", "飞书机器人内部 ID")
	feishuBootstrapCmd.Flags().StringVar(&feishuBotDisplayName, "display-name", "", "飞书机器人展示名")
	feishuBootstrapCmd.Flags().StringVar(&feishuBotAliases, "aliases", "", "逗号分隔的飞书机器人别名")
	feishuBootstrapCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "飞书 app_id")
	feishuBootstrapCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "飞书 app_secret")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapAllowedUsers, "allowed-users", "", "逗号分隔的飞书 open_id 或 union_id 白名单")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapDefaultAgent, "default-agent", "", "该飞书机器人的默认 Agent")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapProgressMode, "progress", "", "该飞书机器人的进度模式")
	feishuBootstrapCmd.Flags().BoolVar(&feishuBootstrapRequireMention, "require-mention-in-group", true, "群聊中是否要求 @ 机器人")
	feishuAddCmd.Flags().StringVar(&feishuBotName, "name", "", "飞书机器人内部 ID")
	feishuAddCmd.Flags().StringVar(&feishuBotDisplayName, "display-name", "", "飞书机器人展示名")
	feishuAddCmd.Flags().StringVar(&feishuBotAliases, "aliases", "", "逗号分隔的飞书机器人别名")
	feishuAddCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "飞书 app_id")
	feishuAddCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "飞书 app_secret")
	feishuAddCmd.Flags().StringVar(&feishuBootstrapAllowedUsers, "allowed-users", "", "逗号分隔的飞书 open_id 或 union_id 白名单")
	feishuAddCmd.Flags().StringVar(&feishuBootstrapDefaultAgent, "default-agent", "", "该飞书机器人的默认 Agent")
	feishuAddCmd.Flags().StringVar(&feishuBootstrapProgressMode, "progress", "", "该飞书机器人的进度模式")
	feishuAddCmd.Flags().BoolVar(&feishuBootstrapRequireMention, "require-mention-in-group", true, "群聊中是否要求 @ 机器人")
	feishuCmd.AddCommand(feishuLoginCmd, feishuStatusCmd, feishuBootstrapCmd, feishuAddCmd, feishuUsersCmd)
	rootCmd.AddCommand(feishuCmd)
}

var feishuCmd = &cobra.Command{
	Use:   "feishu",
	Short: "管理飞书机器人",
}

var feishuLoginCmd = &cobra.Command{
	Use:    "login",
	Short:  "保存并校验飞书应用凭证",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runFeishuLogin(ctx, feishuBotName, feishuLoginAppID, feishuLoginAppSecret)
	},
}

var feishuStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "检查飞书凭证状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runFeishuStatus(ctx, feishuBotName)
	},
}

var feishuBootstrapCmd = &cobra.Command{
	Use:    "bootstrap",
	Short:  "通过参数保存飞书凭证并更新机器人配置",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		var requireMention *bool
		if cmd.Flags().Changed("require-mention-in-group") {
			requireMention = &feishuBootstrapRequireMention
		}
		return runFeishuBootstrap(ctx, feishuBootstrapOptions{
			Name:                  feishuBotName,
			DisplayName:           feishuBotDisplayName,
			Aliases:               splitCSV(feishuBotAliases),
			AppID:                 feishuLoginAppID,
			AppSecret:             feishuLoginAppSecret,
			AllowedUsers:          splitCSV(feishuBootstrapAllowedUsers),
			DefaultAgent:          feishuBootstrapDefaultAgent,
			ProgressMode:          feishuBootstrapProgressMode,
			RequireMentionInGroup: requireMention,
		})
	},
}
