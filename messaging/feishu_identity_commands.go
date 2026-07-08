package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

const feishuIdentityCommandDeniedText = "当前账号未授权管理飞书用户身份，请联系管理员配置 admin_users。"

type feishuIdentityApproveOptions struct {
	Selector string
	BotRef   string
	Admin    bool
}

func isFeishuIdentityCommand(trimmed string) bool {
	return trimmed == "/feishu users" || strings.HasPrefix(trimmed, "/feishu users ")
}

func (h *Handler) handleFeishuIdentityCommand(msg platform.IncomingMessage, trimmed string) string {
	if !h.isAdminMessage(msg) {
		return feishuIdentityCommandDeniedText
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 3 {
		return feishuIdentityUsageText()
	}
	switch fields[2] {
	case "pending":
		return h.renderFeishuIdentityRecords(h.ensureFeishuIdentities().ListPending(), "待确认飞书用户")
	case "list":
		return h.renderFeishuIdentityRecords(h.ensureFeishuIdentities().ListRecords(), "飞书用户身份")
	case "approve":
		return h.handleFeishuIdentityApprove(fields[3:])
	default:
		return feishuIdentityUsageText()
	}
}

func (h *Handler) handleFeishuIdentityApprove(args []string) string {
	opts, err := parseFeishuIdentityApproveOptions(args)
	if err != nil {
		return err.Error()
	}
	store := h.ensureFeishuIdentities()
	record, ok := resolveFeishuIdentityApprovalRecord(store, opts.Selector)
	if !ok {
		return "未找到待确认飞书用户。"
	}
	identity := preferredFeishuAllowedIdentity(record)
	if identity == "" {
		return "该飞书用户缺少可授权身份。"
	}
	bots, err := addFeishuIdentityToConfig(identity, opts.BotRef, opts.Admin)
	if err != nil {
		return fmt.Sprintf("授权失败: %v", err)
	}
	if _, ok := store.Approve(record.Key); !ok {
		return "授权已写入配置，但更新身份状态失败。"
	}
	return renderFeishuIdentityApproved(identity, bots, opts.Admin)
}

func parseFeishuIdentityApproveOptions(args []string) (feishuIdentityApproveOptions, error) {
	if len(args) == 0 {
		return feishuIdentityApproveOptions{}, fmt.Errorf("用法: /feishu users approve <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]")
	}
	opts := feishuIdentityApproveOptions{Selector: strings.TrimSpace(args[0])}
	if isNumericFeishuIdentitySelector(opts.Selector) {
		return opts, fmt.Errorf("为避免列表变化导致误授权，请使用 union_id、user_id 或 open_id。")
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--admin":
			opts.Admin = true
		case "--bot":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return opts, fmt.Errorf("--bot 需要指定机器人 name 或 app_id")
			}
			opts.BotRef = strings.TrimSpace(args[i+1])
			i++
		default:
			return opts, fmt.Errorf("未知参数: %s", args[i])
		}
	}
	return opts, nil
}

func resolveFeishuIdentityApprovalRecord(store *feishuIdentityStore, selector string) (feishuIdentityRecord, bool) {
	pending := store.ListPending()
	for _, record := range pending {
		if feishuIdentityRecordMatches(record, selector) {
			return record, true
		}
	}
	return feishuIdentityRecord{}, false
}

func isNumericFeishuIdentitySelector(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func preferredFeishuAllowedIdentity(record feishuIdentityRecord) string {
	return firstNonBlank(record.UnionID, record.UserID, record.OpenID)
}

func addFeishuIdentityToConfig(identity string, botRef string, admin bool) ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	platformCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	bots, labels, err := addIdentityToFeishuBots(platformCfg.Bots, identity, botRef)
	if err != nil {
		return nil, err
	}
	platformCfg.Bots = bots
	cfg.Platforms[string(platform.PlatformFeishu)] = platformCfg
	if admin {
		cfg.AdminUsers = appendUniqueString(cfg.AdminUsers, identity)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return labels, config.Save(cfg)
}

func addIdentityToFeishuBots(bots []config.FeishuBotConfig, identity string, botRef string) ([]config.FeishuBotConfig, []string, error) {
	if len(bots) == 0 {
		return nil, nil, fmt.Errorf("未配置飞书机器人")
	}
	next := append([]config.FeishuBotConfig(nil), bots...)
	labels := make([]string, 0, len(next))
	for i := range next {
		if botRef != "" && !feishuBotConfigMatchesRef(next[i], botRef) {
			continue
		}
		next[i].AllowedUsers = appendUniqueString(next[i].AllowedUsers, identity)
		labels = append(labels, feishuBotConfigLabel(next[i]))
	}
	if len(labels) == 0 {
		return nil, nil, fmt.Errorf("未找到飞书机器人 %q", botRef)
	}
	return next, labels, nil
}

func feishuBotConfigMatchesRef(bot config.FeishuBotConfig, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == strings.TrimSpace(bot.AppID) {
		return true
	}
	for _, candidate := range config.FeishuBotReferences(bot) {
		if candidate == ref {
			return true
		}
	}
	return false
}

func feishuBotConfigLabel(bot config.FeishuBotConfig) string {
	display := config.FeishuBotDisplayName(bot)
	if display == strings.TrimSpace(bot.Name) {
		return display
	}
	return display + " (" + strings.TrimSpace(bot.Name) + ")"
}

func (h *Handler) renderFeishuIdentityRecords(records []feishuIdentityRecord, title string) string {
	if err := h.ensureFeishuIdentities().LoadError(); err != nil {
		return fmt.Sprintf("读取飞书身份状态失败: %v", err)
	}
	if len(records) == 0 {
		return title + ": 暂无。"
	}
	lines := []string{title + ":"}
	for _, record := range records {
		lines = append(lines, renderFeishuIdentityRecord(record)...)
	}
	return strings.Join(lines, "\n")
}

func renderFeishuIdentityRecord(record feishuIdentityRecord) []string {
	lines := []string{"- " + record.Key}
	if record.UnionID != "" {
		lines = append(lines, "   union_id: "+record.UnionID)
	}
	if record.UserID != "" {
		lines = append(lines, "   user_id: "+record.UserID)
	}
	if record.OpenID != "" {
		lines = append(lines, "   open_id: "+record.OpenID)
	}
	if len(record.Accounts) > 0 {
		lines = append(lines, "   机器人: "+strings.Join(record.Accounts, ", "))
	}
	return lines
}

func renderFeishuIdentityApproved(identity string, bots []string, admin bool) string {
	lines := []string{
		"已授权飞书用户: " + identity,
		"机器人: " + strings.Join(bots, ", "),
		"配置已写入，运行中服务会通过配置热重载生效。",
	}
	if admin {
		lines = append(lines, "已同步加入 admin_users。")
	}
	return strings.Join(lines, "\n")
}

func feishuIdentityUsageText() string {
	return strings.Join([]string{
		"用法:",
		"/feishu users pending",
		"/feishu users list",
		"/feishu users approve <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]",
	}, "\n")
}
