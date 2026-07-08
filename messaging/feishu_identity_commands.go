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

// FeishuIdentityApproveRequest 描述一次飞书身份授权写配置请求。
type FeishuIdentityApproveRequest struct {
	Selector string
	BotRef   string
	Admin    bool
	FilePath string
}

// FeishuIdentityApproveResult 返回已写入配置的身份和机器人范围。
type FeishuIdentityApproveResult struct {
	Identity    string
	Bots        []string
	Admin       bool
	DisplayName string
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
		return h.renderFeishuIdentityViews("待确认飞书用户", true)
	case "list":
		return h.renderFeishuIdentityViews("已授权飞书用户", false)
	case "approve":
		return h.handleFeishuIdentityApprove(fields[3:])
	case "approve-code":
		return h.handleFeishuIdentityApproveCode(fields[3:])
	default:
		return feishuIdentityUsageText()
	}
}

func (h *Handler) handleFeishuIdentityApprove(args []string) string {
	opts, err := parseFeishuIdentityApproveOptions(args)
	if err != nil {
		return err.Error()
	}
	result, err := approveFeishuIdentity(h.ensureFeishuIdentities(), opts)
	if err != nil {
		return err.Error()
	}
	return RenderFeishuIdentityApproval(result)
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
	records := store.ListRecords()
	for _, record := range records {
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

// ApproveFeishuIdentity 从本地自动发现状态确认飞书用户，并写入配置。
func ApproveFeishuIdentity(req FeishuIdentityApproveRequest) (FeishuIdentityApproveResult, error) {
	opts := feishuIdentityApproveOptions{
		Selector: strings.TrimSpace(req.Selector),
		BotRef:   strings.TrimSpace(req.BotRef),
		Admin:    req.Admin,
	}
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(req.FilePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return FeishuIdentityApproveResult{}, err
	}
	return approveFeishuIdentity(store, opts)
}

func approveFeishuIdentity(store *feishuIdentityStore, opts feishuIdentityApproveOptions) (FeishuIdentityApproveResult, error) {
	if isNumericFeishuIdentitySelector(opts.Selector) {
		return FeishuIdentityApproveResult{}, fmt.Errorf("为避免列表变化导致误授权，请使用 union_id、user_id 或 open_id。")
	}
	record, ok := resolveFeishuIdentityApprovalRecord(store, opts.Selector)
	if !ok {
		return FeishuIdentityApproveResult{}, fmt.Errorf("未找到飞书用户身份。")
	}
	if opts.Admin && strings.TrimSpace(record.UnionID) == "" {
		return FeishuIdentityApproveResult{}, fmt.Errorf("该飞书用户缺少 union_id，不能加入 admin_users。")
	}
	identity := preferredFeishuAllowedIdentity(record)
	if identity == "" {
		return FeishuIdentityApproveResult{}, fmt.Errorf("该飞书用户缺少可授权身份。")
	}
	bots, err := addFeishuIdentityToConfig(identity, opts.BotRef, opts.Admin)
	if err != nil {
		return FeishuIdentityApproveResult{}, fmt.Errorf("授权失败: %w", err)
	}
	if _, ok := store.Approve(record.Key); !ok {
		return FeishuIdentityApproveResult{}, fmt.Errorf("授权已写入配置，但更新身份状态失败。")
	}
	return FeishuIdentityApproveResult{Identity: identity, Bots: bots, Admin: opts.Admin}, nil
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

func feishuIdentityUsageText() string {
	return strings.Join([]string{
		"用法:",
		"/feishu users pending",
		"/feishu users list",
		"/feishu users approve <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]",
		"/feishu users approve-code <授权码> [--bot <name|app_id>] [--admin] [--name <显示名>]",
		"--admin 只会写入 union_id；缺少 union_id 时会拒绝。",
	}, "\n")
}
