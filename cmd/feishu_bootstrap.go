package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
)

type feishuBootstrapOptions struct {
	Name                  string
	DisplayName           string
	Aliases               []string
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
	opts.DisplayName = strings.TrimSpace(opts.DisplayName)
	opts.AppID = strings.TrimSpace(opts.AppID)
	opts.AppSecret = strings.TrimSpace(opts.AppSecret)
	opts.DefaultAgent = strings.TrimSpace(opts.DefaultAgent)
	opts.ProgressMode = strings.TrimSpace(opts.ProgressMode)
	if opts.Name == "" {
		return fmt.Errorf("--name is required")
	}
	resolvedName, err := resolveFeishuBotName(opts.Name)
	if err != nil {
		return err
	}
	opts.Name = resolvedName
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
		DisplayName:           opts.DisplayName,
		Aliases:               opts.Aliases,
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
	next.MaxMessageAgeSeconds = existing.MaxMessageAgeSeconds
	if len(opts.AllowedUsers) == 0 {
		next.AllowedUsers = existing.AllowedUsers
	}
	if opts.DefaultAgent == "" {
		next.DefaultAgent = existing.DefaultAgent
	}
	if opts.DisplayName == "" {
		next.DisplayName = existing.DisplayName
	}
	if len(opts.Aliases) == 0 {
		next.Aliases = existing.Aliases
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
	if opts.DisplayName != "" {
		fmt.Printf("显示名：%s\n", opts.DisplayName)
	}
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
