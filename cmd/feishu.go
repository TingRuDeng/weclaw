package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/spf13/cobra"
)

var (
	feishuLoginAppID     string
	feishuLoginAppSecret string
	validateFeishuCreds  = feishu.ValidateCredentials
	saveFeishuCreds      = feishu.SaveCredentials
)

func init() {
	feishuLoginCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "Feishu app_id")
	feishuLoginCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "Feishu app_secret")
	feishuCmd.AddCommand(feishuLoginCmd, feishuStatusCmd)
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
		return runFeishuLogin(ctx, feishuLoginAppID, feishuLoginAppSecret)
	},
}

var feishuStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check Feishu credential status",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runFeishuStatus(ctx)
	},
}

// runFeishuLogin 校验飞书凭证后保存到专用凭证文件，避免 secret 进入 config.json。
func runFeishuLogin(ctx context.Context, appID string, appSecret string) error {
	creds := feishu.Credentials{AppID: appID, AppSecret: appSecret}
	if err := validateFeishuCreds(ctx, creds); err != nil {
		return err
	}
	if err := saveFeishuCreds(creds); err != nil {
		return err
	}
	path, err := feishu.CredentialsPath()
	if err != nil {
		return err
	}
	fmt.Printf("飞书凭证已保存：%s\n", path)
	fmt.Printf("App ID: %s\n", creds.AppID)
	return nil
}

// runFeishuStatus 读取并校验飞书凭证，输出不包含 app_secret 的状态。
func runFeishuStatus(ctx context.Context) error {
	record, err := feishu.LoadCredentialsWithSource()
	if err != nil {
		return err
	}
	if err := validateFeishuCreds(ctx, record.Credentials); err != nil {
		return err
	}
	fmt.Printf("飞书凭证有效\n")
	fmt.Printf("来源：%s\n", record.Source)
	if record.Path != "" {
		fmt.Printf("路径：%s\n", record.Path)
	}
	fmt.Printf("App ID: %s\n", record.Credentials.AppID)
	return nil
}
