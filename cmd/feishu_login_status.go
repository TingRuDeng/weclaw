package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/feishu"
)

// runFeishuLogin 校验飞书凭证后保存到专用凭证文件，避免 secret 进入 config.json。
func runFeishuLogin(ctx context.Context, name string, appID string, appSecret string) error {
	resolvedName, err := resolveFeishuBotName(name)
	if err != nil {
		return err
	}
	creds := feishu.Credentials{AppID: appID, AppSecret: appSecret}
	if err := validateFeishuCreds(ctx, creds); err != nil {
		return err
	}
	if err := saveFeishuCreds(resolvedName, creds); err != nil {
		return err
	}
	path, err := feishu.CredentialsPathForBot(resolvedName)
	if err != nil {
		return err
	}
	fmt.Printf("飞书凭证已保存：%s\n", path)
	fmt.Printf("Bot: %s\n", resolvedName)
	fmt.Printf("App ID: %s\n", creds.AppID)
	return nil
}

// runFeishuStatus 读取并校验飞书凭证，输出不包含 app_secret 的状态。
func runFeishuStatus(ctx context.Context, name string) error {
	bot, matched, err := resolveFeishuBotRef(name)
	if err != nil {
		return err
	}
	resolvedName := strings.TrimSpace(name)
	if matched {
		resolvedName = bot.Name
	}
	record, err := feishu.LoadCredentialsWithSourceForBot(resolvedName)
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
	fmt.Printf("Bot: %s\n", resolvedName)
	if matched && strings.TrimSpace(bot.DisplayName) != "" {
		fmt.Printf("显示名：%s\n", bot.DisplayName)
	}
	fmt.Printf("App ID: %s\n", record.Credentials.AppID)
	return nil
}
