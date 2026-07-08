package messaging

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

func (h *Handler) renderFeishuIdentityViews(title string, pendingOnly bool) string {
	store := h.ensureFeishuIdentities()
	if err := store.LoadError(); err != nil {
		return fmt.Sprintf("读取飞书身份状态失败: %v", err)
	}
	cfg, cfgOK := loadFeishuIdentityConfig()
	labels := feishuBotAccountLabelsForConfig(cfg, cfgOK)
	views := feishuIdentityViewsForMessage(store.ListRecords(), cfg, cfgOK, pendingOnly)
	if len(views) == 0 {
		return title + ": 暂无。"
	}
	lines := []string{title + ":"}
	for _, view := range views {
		lines = append(lines, renderFeishuIdentityViewForMessage(view, labels, pendingOnly)...)
	}
	return strings.Join(lines, "\n")
}

func feishuIdentityViewsForMessage(records []feishuIdentityRecord, cfg config.Config, cfgOK bool, pendingOnly bool) []FeishuIdentityView {
	views := make([]FeishuIdentityView, 0, len(records))
	for _, record := range records {
		view := feishuIdentityViewFromRecord(record, cfg, cfgOK)
		if pendingOnly && !feishuIdentityViewNeedsApproval(view, record) {
			continue
		}
		if !pendingOnly && len(view.AuthorizedAccounts) == 0 {
			continue
		}
		views = append(views, view)
	}
	return views
}

func renderFeishuIdentityViewForMessage(view FeishuIdentityView, labels map[string]string, showApprovalCode bool) []string {
	lines := []string{"- " + view.Key}
	lines = append(lines, feishuIdentityIDLines(view)...)
	if !showApprovalCode && len(view.AuthorizedAccounts) > 0 {
		lines = append(lines, "   已授权机器人: "+strings.Join(feishuAccountLabels(view.AuthorizedAccounts, labels), ", "))
	}
	if showApprovalCode && len(view.UnauthorizedAccounts) > 0 {
		lines = append(lines, "   待授权机器人: "+strings.Join(feishuAccountLabels(view.UnauthorizedAccounts, labels), ", "))
	}
	lines = append(lines, "   状态: "+feishuIdentityMessageStatus(view, showApprovalCode))
	lines = append(lines, feishuIdentityActionLines(view, showApprovalCode)...)
	return lines
}

func feishuIdentityIDLines(view FeishuIdentityView) []string {
	lines := []string{}
	if view.UnionID != "" {
		lines = append(lines, "   union_id: "+view.UnionID)
	}
	if view.UserID != "" {
		lines = append(lines, "   user_id: "+view.UserID)
	}
	if view.OpenID != "" {
		lines = append(lines, "   open_id: "+view.OpenID)
	}
	return lines
}

func feishuIdentityActionLines(view FeishuIdentityView, showApprovalCode bool) []string {
	if showApprovalCode && strings.TrimSpace(view.AuthCode) != "" {
		return feishuIdentityAuthCodeLines(view)
	}
	if showApprovalCode && len(view.UnauthorizedAccounts) > 0 {
		return feishuIdentityApproveHintLines(view)
	}
	return nil
}

func feishuIdentityAuthCodeLines(view FeishuIdentityView) []string {
	lines := []string{
		"   授权码: " + view.AuthCode,
		"   授权命令: /feishu users approve-code " + view.AuthCode,
	}
	if strings.TrimSpace(view.UnionID) != "" {
		lines = append(lines, "   授权并设为管理员: /feishu users approve-code "+view.AuthCode+" --admin")
	}
	return lines
}

func feishuIdentityApproveHintLines(view FeishuIdentityView) []string {
	selector := firstNonBlank(view.UnionID, view.UserID, view.OpenID, view.Key)
	if strings.TrimSpace(selector) == "" {
		return nil
	}
	lines := []string{
		"   授权命令: /feishu users approve " + selector,
		"   授权说明: 执行上面的授权命令可授权该用户访问待授权机器人。",
	}
	if strings.TrimSpace(view.UnionID) != "" {
		lines = append(lines,
			"   管理员命令: /feishu users approve "+view.UnionID+" --admin",
			"   管理员说明: 需要同时设为管理员时执行上面的管理员命令。",
		)
	}
	return lines
}

func feishuIdentityMessageStatus(view FeishuIdentityView, showApprovalCode bool) string {
	if showApprovalCode && strings.TrimSpace(view.AuthCode) != "" {
		return "待确认"
	}
	if showApprovalCode && len(view.UnauthorizedAccounts) > 0 {
		return "待授权"
	}
	if len(view.AuthorizedAccounts) > 0 {
		return "已授权"
	}
	return "待确认"
}

func feishuBotAccountLabelsForConfig(cfg config.Config, ok bool) map[string]string {
	if !ok {
		return nil
	}
	labels := make(map[string]string)
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if appID := strings.TrimSpace(bot.AppID); appID != "" {
			labels[appID] = feishuBotConfigLabel(bot)
		}
	}
	return labels
}

func feishuAccountLabels(accounts []string, labels map[string]string) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if label := strings.TrimSpace(labels[account]); label != "" {
			out = append(out, label+" ("+account+")")
			continue
		}
		out = append(out, account)
	}
	return out
}
