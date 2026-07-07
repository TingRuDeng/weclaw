package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
	"github.com/spf13/cobra"
)

var (
	feishuBotName                 string
	feishuLoginAppID              string
	feishuLoginAppSecret          string
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
	feishuBootstrapCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "Feishu app_id")
	feishuBootstrapCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "Feishu app_secret")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapAllowedUsers, "allowed-users", "", "Comma-separated Feishu open_id or union_id allowlist")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapDefaultAgent, "default-agent", "", "Default agent for this Feishu bot")
	feishuBootstrapCmd.Flags().StringVar(&feishuBootstrapProgressMode, "progress", "", "Progress mode for this Feishu bot")
	feishuBootstrapCmd.Flags().BoolVar(&feishuBootstrapRequireMention, "require-mention-in-group", true, "Require @bot in group chats")
	feishuAddCmd.Flags().StringVar(&feishuBotName, "name", "", "飞书机器人名称")
	feishuAddCmd.Flags().StringVar(&feishuLoginAppID, "app-id", "", "飞书 app_id")
	feishuAddCmd.Flags().StringVar(&feishuLoginAppSecret, "app-secret", "", "飞书 app_secret")
	feishuAddCmd.Flags().StringVar(&feishuBootstrapAllowedUsers, "allowed-users", "", "逗号分隔的飞书 open_id 或 union_id 白名单")
	feishuAddCmd.Flags().StringVar(&feishuBootstrapDefaultAgent, "default-agent", "", "该飞书机器人的默认 Agent")
	feishuAddCmd.Flags().StringVar(&feishuBootstrapProgressMode, "progress", "", "该飞书机器人的进度模式")
	feishuAddCmd.Flags().BoolVar(&feishuBootstrapRequireMention, "require-mention-in-group", true, "群聊中是否要求 @ 机器人")
	feishuCmd.AddCommand(feishuLoginCmd, feishuStatusCmd, feishuBootstrapCmd, feishuAddCmd)
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
			AppID:                 feishuLoginAppID,
			AppSecret:             feishuLoginAppSecret,
			AllowedUsers:          splitCSV(feishuBootstrapAllowedUsers),
			DefaultAgent:          feishuBootstrapDefaultAgent,
			ProgressMode:          feishuBootstrapProgressMode,
			RequireMentionInGroup: requireMention,
		})
	},
}

// runFeishuLogin 校验飞书凭证后保存到专用凭证文件，避免 secret 进入 config.json。
func runFeishuLogin(ctx context.Context, name string, appID string, appSecret string) error {
	creds := feishu.Credentials{AppID: appID, AppSecret: appSecret}
	if err := validateFeishuCreds(ctx, creds); err != nil {
		return err
	}
	if err := saveFeishuCreds(name, creds); err != nil {
		return err
	}
	path, err := feishu.CredentialsPathForBot(name)
	if err != nil {
		return err
	}
	fmt.Printf("飞书凭证已保存：%s\n", path)
	fmt.Printf("Bot: %s\n", name)
	fmt.Printf("App ID: %s\n", creds.AppID)
	return nil
}

type feishuBootstrapOptions struct {
	Name                  string
	AppID                 string
	AppSecret             string
	AllowedUsers          []string
	DefaultAgent          string
	ProgressMode          string
	RequireMentionInGroup *bool
}

// runFeishuBootstrap 把飞书凭证和 WeClaw bot 配置一起落盘，降低首次配置成本。
func runFeishuBootstrap(ctx context.Context, opts feishuBootstrapOptions) error {
	opts.Name = strings.TrimSpace(opts.Name)
	opts.AppID = strings.TrimSpace(opts.AppID)
	opts.AppSecret = strings.TrimSpace(opts.AppSecret)
	opts.DefaultAgent = strings.TrimSpace(opts.DefaultAgent)
	opts.ProgressMode = strings.TrimSpace(opts.ProgressMode)
	if opts.Name == "" {
		return fmt.Errorf("--name is required")
	}
	if err := validateProgressMode(opts.ProgressMode); err != nil {
		return err
	}
	if err := validateFeishuCreds(ctx, feishu.Credentials{AppID: opts.AppID, AppSecret: opts.AppSecret}); err != nil {
		return err
	}
	if err := saveFeishuCreds(opts.Name, feishu.Credentials{AppID: opts.AppID, AppSecret: opts.AppSecret}); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	upsertFeishuBotConfig(cfg, opts)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	printFeishuBootstrapResult(opts)
	return nil
}

// upsertFeishuBotConfig 按 bot name 新增或更新飞书入口，并开启飞书平台。
func upsertFeishuBotConfig(cfg *config.Config, opts feishuBootstrapOptions) {
	if cfg.Platforms == nil {
		cfg.Platforms = make(map[string]config.PlatformConfig)
	}
	enabled := true
	platformCfg := cfg.Platforms["feishu"]
	platformCfg.Enabled = &enabled
	bot := config.FeishuBotConfig{
		Name:                  opts.Name,
		AppID:                 opts.AppID,
		AllowedUsers:          opts.AllowedUsers,
		DefaultAgent:          opts.DefaultAgent,
		RequireMentionInGroup: opts.RequireMentionInGroup,
	}
	if opts.ProgressMode != "" {
		bot.Progress = &config.ProgressConfig{Mode: opts.ProgressMode}
	}
	for i := range platformCfg.Bots {
		if platformCfg.Bots[i].Name == opts.Name {
			bot = mergeFeishuBootstrapBot(platformCfg.Bots[i], bot, opts)
			platformCfg.Bots[i] = bot
			cfg.Platforms["feishu"] = platformCfg
			return
		}
	}
	platformCfg.Bots = append(platformCfg.Bots, bot)
	cfg.Platforms["feishu"] = platformCfg
}

// mergeFeishuBootstrapBot 保留本次 bootstrap 未显式覆盖的既有 bot 配置。
func mergeFeishuBootstrapBot(existing config.FeishuBotConfig, next config.FeishuBotConfig, opts feishuBootstrapOptions) config.FeishuBotConfig {
	if len(opts.AllowedUsers) == 0 {
		next.AllowedUsers = existing.AllowedUsers
	}
	if opts.DefaultAgent == "" {
		next.DefaultAgent = existing.DefaultAgent
	}
	if opts.ProgressMode == "" {
		next.Progress = existing.Progress
	}
	if opts.RequireMentionInGroup == nil {
		next.RequireMentionInGroup = existing.RequireMentionInGroup
	}
	return next
}

// printFeishuBootstrapResult 输出不含 app_secret 的配置结果和后续诊断提示。
func printFeishuBootstrapResult(opts feishuBootstrapOptions) {
	fmt.Println("飞书 bootstrap 完成")
	fmt.Printf("Bot: %s\n", opts.Name)
	fmt.Printf("App ID: %s\n", opts.AppID)
	fmt.Println("已更新：~/.weclaw/config.json")
	if path, err := feishu.CredentialsPathForBot(opts.Name); err == nil {
		fmt.Printf("已保存：%s\n", path)
	}
	if path, err := exec.LookPath("lark-cli"); err == nil {
		fmt.Printf("检测到 lark-cli：%s\n", path)
		fmt.Println("建议继续用 lark-cli 检查应用权限、事件订阅和消息发送能力。")
		return
	}
	fmt.Println("未检测到 lark-cli；后续可安装 larksuite/cli 辅助检查权限和事件。")
}

// validateProgressMode 拒绝未知进度模式，避免写入启动后才失败的配置。
func validateProgressMode(mode string) error {
	switch mode {
	case "", "off", "typing", "summary", "verbose", "stream", "debug":
		return nil
	default:
		return fmt.Errorf("invalid progress mode %q", mode)
	}
}

// splitCSV 解析命令行逗号分隔参数，并忽略空片段。
func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// runFeishuStatus 读取并校验飞书凭证，输出不包含 app_secret 的状态。
func runFeishuStatus(ctx context.Context, name string) error {
	record, err := feishu.LoadCredentialsWithSourceForBot(name)
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
	fmt.Printf("Bot: %s\n", name)
	fmt.Printf("App ID: %s\n", record.Credentials.AppID)
	return nil
}
