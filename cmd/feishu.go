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
	feishuLoginCmd.Flags().StringVar(&feishuBotName, "name", "", "Feishu bot name")
	feishuLoginCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "Feishu app_id")
	feishuLoginCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "Feishu app_secret")
	feishuStatusCmd.Flags().StringVar(&feishuBotName, "name", "", "Feishu bot name")
	feishuBootstrapCmd.Flags().StringVar(&feishuBotName, "name", "", "Feishu bot name")
	feishuBootstrapCmd.Flags().StringVar(&feishuBotDisplayName, "display-name", "", "Feishu bot display name")
	feishuBootstrapCmd.Flags().StringVar(&feishuBotAliases, "aliases", "", "Comma-separated Feishu bot aliases")
	feishuBootstrapCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "Feishu app_id")
	feishuBootstrapCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "Feishu app_secret")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapAllowedUsers, "allowed-users", "", "Comma-separated Feishu open_id or union_id allowlist")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapDefaultAgent, "default-agent", "", "Default agent for this Feishu bot")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapProgressMode, "progress", "", "Progress mode for this Feishu bot")
	feishuBootstrapCmd.Flags().BoolVar(&feishuBootstrapRequireMention, "require-mention-in-group", true, "Require @bot in group chats")
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
	Short: "Manage Feishu platform credentials",
}

var feishuLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Save and validate Feishu app credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runFeishuLogin(ctx, feishuBotName, feishuLoginAppID, feishuLoginAppSecret)
	},
}

var feishuStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check Feishu credential status",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runFeishuStatus(ctx, feishuBotName)
	},
}

var feishuBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Save Feishu credentials and update WeClaw bot config",
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
