package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

const feishuIdentityCommandDeniedText = "当前账号未授权管理飞书用户身份，请将该身份加入当前机器人的 allowed_users。"

type feishuIdentityApproveOptions struct {
	Selector  string
	BotRef    string
	AccountID string
}

// FeishuIdentityApproveRequest 描述一次飞书身份授权写配置请求。
type FeishuIdentityApproveRequest struct {
	Selector string
	BotRef   string
	FilePath string
}

// FeishuIdentityApproveResult 返回已写入配置的身份和机器人范围。
type FeishuIdentityApproveResult struct {
	Identity     string
	Bots         []string
	DisplayName  string
	allowedUsers []string
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
		return h.renderFeishuIdentityViewsForAccount("待确认飞书用户", true, msg.AccountID)
	case "list":
		return h.renderFeishuIdentityViewsForAccount("已授权飞书用户", false, msg.AccountID)
	case "approve":
		return h.handleFeishuIdentityApprove(msg, fields[3:])
	case "approve-code":
		return h.handleFeishuIdentityApproveCode(msg, fields[3:])
	case "revoke":
		return h.handleFeishuIdentityRevoke(msg, fields[3:])
	default:
		return feishuIdentityUsageText()
	}
}

func (h *Handler) handleFeishuIdentityApprove(msg platform.IncomingMessage, args []string) string {
	opts, err := parseFeishuIdentityApproveOptions(args)
	if err != nil {
		return err.Error()
	}
	opts.BotRef, err = remoteFeishuIdentityBotRef(msg.AccountID, opts.BotRef)
	if err != nil {
		return err.Error()
	}
	opts.AccountID = strings.TrimSpace(msg.AccountID)
	h.feishuIdentityMutationMu.Lock()
	defer h.feishuIdentityMutationMu.Unlock()
	result, err := approveFeishuIdentity(h.ensureFeishuIdentities(), opts)
	if err != nil {
		return err.Error()
	}
	h.refreshFeishuAccountAccess(opts.AccountID, result.allowedUsers)
	h.auditFeishuIdentityMutation(msg, "feishu_identity_approve", result.Identity, result.Bots)
	return RenderFeishuIdentityApproval(result)
}

// remoteFeishuIdentityBotRef 把聊天内用户管理命令强制限制在消息来源机器人。
// 本地 CLI 仍通过显式请求结构走独立作用域，不复用这条远程能力。
func remoteFeishuIdentityBotRef(accountID string, requested string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	requested = strings.TrimSpace(requested)
	if accountID == "" {
		return "", fmt.Errorf("无法确认当前飞书机器人账号，本次操作未执行。")
	}
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("读取飞书机器人配置失败: %w", err)
	}
	bot, ok := feishuBotByAppID(cfg.Platforms[string(platform.PlatformFeishu)].Bots, accountID)
	if !ok {
		return "", fmt.Errorf("当前飞书机器人未出现在配置中，本次操作未执行。")
	}
	if requested != "" && !feishuBotConfigMatchesRef(bot, requested) {
		return "", fmt.Errorf("飞书内只能管理当前机器人；如需管理其他机器人，请从对应机器人窗口操作。")
	}
	return accountID, nil
}

func (h *Handler) auditFeishuIdentityMutation(msg platform.IncomingMessage, action string, identity string, bots []string) {
	actor := strings.TrimSpace(msg.UserID)
	if identity, ok := msg.AuthorizedIdentity(); ok {
		actor = strings.TrimSpace(identity)
	}
	h.auditRecord(auditEntry{
		Platform: string(msg.Platform), User: actor, Action: action,
		Summary: fmt.Sprintf("target=%s source_account=%s bots=%s", strings.TrimSpace(identity), strings.TrimSpace(msg.AccountID), strings.Join(bots, ",")),
	})
}

func parseFeishuIdentityApproveOptions(args []string) (feishuIdentityApproveOptions, error) {
	if len(args) == 0 {
		return feishuIdentityApproveOptions{}, fmt.Errorf("用法: /feishu users approve <union_id|user_id|open_id> [--bot <name|app_id>]")
	}
	opts := feishuIdentityApproveOptions{Selector: strings.TrimSpace(args[0])}
	if isNumericFeishuIdentitySelector(opts.Selector) {
		return opts, fmt.Errorf("为避免列表变化导致误授权，请使用 union_id、user_id 或 open_id。")
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
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
	if opts.AccountID != "" && !feishuIdentityRecordSeenOnAccount(record, opts.AccountID) {
		return FeishuIdentityApproveResult{}, fmt.Errorf("未在当前机器人发现该飞书用户身份。")
	}
	identity := preferredFeishuAllowedIdentity(record)
	if identity == "" {
		return FeishuIdentityApproveResult{}, fmt.Errorf("该飞书用户缺少可授权身份。")
	}
	bots, allowedUsers, err := addFeishuIdentityToConfig(identity, opts.BotRef)
	if err != nil {
		return FeishuIdentityApproveResult{}, fmt.Errorf("授权失败: %w", err)
	}
	if _, ok := store.Approve(record.Key); !ok {
		return FeishuIdentityApproveResult{}, fmt.Errorf("授权已写入配置，但更新身份状态失败。")
	}
	return FeishuIdentityApproveResult{Identity: identity, Bots: bots, allowedUsers: allowedUsers}, nil
}

func feishuIdentityRecordSeenOnAccount(record feishuIdentityRecord, accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return true
	}
	if stringSliceContains(record.Accounts, accountID) {
		return true
	}
	return strings.TrimSpace(record.OpenIDs[accountID]) != ""
}

func addFeishuIdentityToConfig(identity string, botRef string) ([]string, []string, error) {
	var labels []string
	var allowedUsers []string
	err := config.Update(func(cfg *config.Config) error {
		platformCfg := cfg.Platforms[string(platform.PlatformFeishu)]
		bots, updatedLabels, err := addIdentityToFeishuBots(platformCfg.Bots, identity, botRef)
		if err != nil {
			return err
		}
		labels = updatedLabels
		allowedUsers = feishuAllowedUsersForBotRef(bots, botRef)
		platformCfg.Bots = bots
		cfg.Platforms[string(platform.PlatformFeishu)] = platformCfg
		return nil
	})
	return labels, allowedUsers, err
}

func feishuAllowedUsersForBotRef(bots []config.FeishuBotConfig, botRef string) []string {
	for _, bot := range bots {
		if strings.TrimSpace(botRef) != "" && !feishuBotConfigMatchesRef(bot, botRef) {
			continue
		}
		return append([]string(nil), bot.AllowedUsers...)
	}
	return nil
}

func addIdentityToFeishuBots(bots []config.FeishuBotConfig, identity string, botRef string) ([]config.FeishuBotConfig, []string, error) {
	if len(bots) == 0 {
		return nil, nil, fmt.Errorf("未配置飞书机器人")
	}
	if len(bots) > 1 && strings.TrimSpace(botRef) == "" {
		return nil, nil, fmt.Errorf("配置了多个飞书机器人，请使用 --bot <name|app_id> 明确目标机器人")
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
		"飞书用户管理:",
		"/feishu users pending 查看待授权用户，并显示可复制授权命令",
		"/feishu users list 查看已授权用户和机器人范围",
		"/feishu users approve <union_id|user_id|open_id> 直接授权用户",
		"/feishu users approve-code <授权码> 授权用户",
		"/feishu users revoke <用户ID> 取消用户访问授权",
		"可选: --bot <name|app_id> 限定某个机器人。",
	}, "\n")
}
