package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

type feishuIdentityRevokeOptions struct {
	Selector string
	BotRef   string
	Admin    bool
}

// FeishuIdentityRevokeRequest 描述一次飞书身份取消授权请求。
type FeishuIdentityRevokeRequest struct {
	Selector string
	BotRef   string
	Admin    bool
	FilePath string
}

// FeishuIdentityRevokeResult 返回已移除授权的身份和范围。
type FeishuIdentityRevokeResult struct {
	Identity string
	Bots     []string
	Admin    bool
}

func (h *Handler) handleFeishuIdentityRevoke(args []string) string {
	opts, err := parseFeishuIdentityRevokeOptions(args)
	if err != nil {
		return err.Error()
	}
	result, err := revokeFeishuIdentity(h.ensureFeishuIdentities(), opts)
	if err != nil {
		return err.Error()
	}
	return RenderFeishuIdentityRevoke(result)
}

func parseFeishuIdentityRevokeOptions(args []string) (feishuIdentityRevokeOptions, error) {
	if len(args) == 0 {
		return feishuIdentityRevokeOptions{}, fmt.Errorf("用法: /feishu users revoke <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]")
	}
	opts := feishuIdentityRevokeOptions{Selector: strings.TrimSpace(args[0])}
	if isNumericFeishuIdentitySelector(opts.Selector) {
		return opts, fmt.Errorf("为避免列表变化导致误操作，请使用 union_id、user_id 或 open_id。")
	}
	for i := 1; i < len(args); i++ {
		next, skip, err := applyFeishuRevokeFlag(opts, args, i)
		if err != nil {
			return opts, err
		}
		opts = next
		i += skip
	}
	return opts, nil
}

func applyFeishuRevokeFlag(opts feishuIdentityRevokeOptions, args []string, index int) (feishuIdentityRevokeOptions, int, error) {
	switch args[index] {
	case "--admin":
		opts.Admin = true
		return opts, 0, nil
	case "--bot":
		if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
			return opts, 0, fmt.Errorf("--bot 需要指定机器人 name 或 app_id")
		}
		opts.BotRef = strings.TrimSpace(args[index+1])
		return opts, 1, nil
	default:
		return opts, 0, fmt.Errorf("未知参数: %s", args[index])
	}
}

// RevokeFeishuIdentity 从本地配置移除飞书用户授权。
func RevokeFeishuIdentity(req FeishuIdentityRevokeRequest) (FeishuIdentityRevokeResult, error) {
	opts := feishuIdentityRevokeOptions{
		Selector: strings.TrimSpace(req.Selector),
		BotRef:   strings.TrimSpace(req.BotRef),
		Admin:    req.Admin,
	}
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(req.FilePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return FeishuIdentityRevokeResult{}, err
	}
	return revokeFeishuIdentity(store, opts)
}

func revokeFeishuIdentity(store *feishuIdentityStore, opts feishuIdentityRevokeOptions) (FeishuIdentityRevokeResult, error) {
	if isNumericFeishuIdentitySelector(opts.Selector) {
		return FeishuIdentityRevokeResult{}, fmt.Errorf("为避免列表变化导致误操作，请使用 union_id、user_id 或 open_id。")
	}
	identity, keys := feishuIdentityRevokeKeys(store, opts.Selector)
	bots, adminRemoved, err := removeFeishuIdentityFromConfig(keys, opts.BotRef, opts.Admin)
	if err != nil {
		return FeishuIdentityRevokeResult{}, fmt.Errorf("取消授权失败: %w", err)
	}
	if len(bots) == 0 && !adminRemoved {
		return FeishuIdentityRevokeResult{}, fmt.Errorf("未找到该飞书用户授权。")
	}
	return FeishuIdentityRevokeResult{Identity: identity, Bots: bots, Admin: adminRemoved}, nil
}

func feishuIdentityRevokeKeys(store *feishuIdentityStore, selector string) (string, []string) {
	selector = strings.TrimSpace(selector)
	record, ok := resolveFeishuIdentityApprovalRecord(store, selector)
	if !ok {
		return selector, []string{selector}
	}
	identity := firstNonBlank(preferredFeishuAllowedIdentity(record), selector)
	return identity, feishuIdentityAuthKeys(record, "")
}

func removeFeishuIdentityFromConfig(keys []string, botRef string, admin bool) ([]string, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, false, err
	}
	platformCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	bots, labels, err := removeIdentityFromFeishuBots(platformCfg.Bots, keys, botRef)
	if err != nil {
		return nil, false, err
	}
	platformCfg.Bots = bots
	cfg.Platforms[string(platform.PlatformFeishu)] = platformCfg
	adminRemoved := false
	if admin {
		cfg.AdminUsers, adminRemoved = removeIdentityValues(cfg.AdminUsers, keys)
	}
	if len(labels) == 0 && !adminRemoved {
		return nil, false, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, err
	}
	return labels, adminRemoved, config.Save(cfg)
}

func removeIdentityFromFeishuBots(bots []config.FeishuBotConfig, keys []string, botRef string) ([]config.FeishuBotConfig, []string, error) {
	if len(bots) == 0 {
		return nil, nil, fmt.Errorf("未配置飞书机器人")
	}
	next := append([]config.FeishuBotConfig(nil), bots...)
	labels := make([]string, 0, len(next))
	matched := false
	for i := range next {
		if botRef != "" && !feishuBotConfigMatchesRef(next[i], botRef) {
			continue
		}
		matched = true
		allowed, removed := removeIdentityValues(next[i].AllowedUsers, keys)
		if removed {
			next[i].AllowedUsers = allowed
			labels = append(labels, feishuBotConfigLabel(next[i]))
		}
	}
	if !matched {
		return nil, nil, fmt.Errorf("未找到飞书机器人 %q", botRef)
	}
	return next, labels, nil
}

func removeIdentityValues(values []string, keys []string) ([]string, bool) {
	removeSet := stringSet(keys)
	next := make([]string, 0, len(values))
	removed := false
	for _, value := range values {
		if removeSet[strings.TrimSpace(value)] {
			removed = true
			continue
		}
		next = append(next, value)
	}
	return next, removed
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	return set
}

// RenderFeishuIdentityRevoke 渲染取消授权后的用户可见结果。
func RenderFeishuIdentityRevoke(result FeishuIdentityRevokeResult) string {
	lines := []string{"已取消飞书用户授权: " + result.Identity}
	if len(result.Bots) > 0 {
		lines = append(lines, "已移除机器人授权: "+strings.Join(result.Bots, ", "))
	}
	if result.Admin {
		lines = append(lines, "已移出 admin_users")
	}
	return strings.Join(lines, "\n")
}
