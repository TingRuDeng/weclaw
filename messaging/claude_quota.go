package messaging

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// renderClaudeQuota 查询并渲染 Claude 账号额度。
func (h *Handler) renderClaudeQuota(ctx context.Context, ag agent.Agent) string {
	quotaAg, ok := ag.(agent.ClaudeQuotaAgent)
	if !ok {
		return "当前 Claude Agent 不支持额度查询。"
	}
	quota, err := quotaAg.ReadClaudeQuota(ctx)
	if err != nil {
		return "查询 Claude 额度失败，请确认本机 Claude Code 已登录并稍后重试。"
	}
	return renderClaudeQuotaText(quota)
}

func renderClaudeQuotaText(quota agent.ClaudeQuota) string {
	lines := []string{"Claude 账号额度:"}
	if plan := strings.TrimSpace(quota.SubscriptionType); plan != "" {
		lines = append(lines, "plan: "+plan)
	}
	if !quota.RateLimitsAvailable {
		lines = append(lines, "当前登录方式未提供 Claude 订阅额度；API key、第三方 provider 或缺少 profile 权限时会出现此状态。")
		return wechatCommandText(lines...)
	}
	if len(quota.Limits) == 0 {
		lines = append(lines, "Claude 当前没有返回可展示的额度窗口。")
	} else {
		for _, limit := range quota.Limits {
			lines = append(lines, claudeQuotaWindowLine(limit))
		}
	}
	if quota.ExtraUsage != nil {
		lines = append(lines, claudeExtraUsageLine(*quota.ExtraUsage))
	}
	return wechatCommandText(lines...)
}

func claudeQuotaWindowLine(limit agent.ClaudeRateLimit) string {
	label := claudeQuotaLimitLabel(limit)
	usage := label + ": 使用比例未返回"
	if limit.UsedPercent != nil {
		usage = label + ": 已用 " + claudeQuotaPercent(limit.UsedPercent)
	}
	parts := []string{usage}
	if reset := formatClaudeResetTime(limit.ResetsAt); reset != "" {
		parts = append(parts, "重置 "+reset)
	}
	return strings.Join(parts, "，")
}

func claudeQuotaLimitLabel(limit agent.ClaudeRateLimit) string {
	name := strings.TrimSpace(limit.Name)
	if name != "" {
		return name
	}
	switch strings.TrimSpace(limit.ID) {
	case "five_hour":
		return "5 小时"
	case "seven_day":
		return "7 天（全部模型）"
	case "seven_day_oauth_apps":
		return "7 天（OAuth 应用）"
	case "seven_day_opus":
		return "7 天（Opus）"
	case "seven_day_sonnet":
		return "7 天（Sonnet）"
	case "model_scoped":
		return "模型周额度"
	default:
		if strings.TrimSpace(limit.ID) == "" {
			return "额度窗口"
		}
		return limit.ID
	}
}

func claudeQuotaPercent(value *float64) string {
	if value == nil {
		return "未返回"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64) + "%"
}

func claudeExtraUsageLine(extra agent.ClaudeExtraUsage) string {
	if !extra.Enabled {
		return "额外用量: 未启用"
	}
	parts := []string{"额外用量: 已启用"}
	if extra.UsedCredits != nil || extra.MonthlyLimit != nil {
		used := "未返回"
		limit := "未返回"
		if extra.UsedCredits != nil {
			used = strconv.FormatFloat(*extra.UsedCredits, 'f', -1, 64)
		}
		if extra.MonthlyLimit != nil {
			limit = strconv.FormatFloat(*extra.MonthlyLimit, 'f', -1, 64)
		}
		amount := "额度 " + used + " / " + limit
		if currency := strings.TrimSpace(extra.Currency); currency != "" {
			amount += " " + currency
		}
		parts = append(parts, amount)
	}
	if extra.UsedPercent != nil {
		parts = append(parts, "已用 "+claudeQuotaPercent(extra.UsedPercent))
	}
	return strings.Join(parts, "，")
}

func formatClaudeResetTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04")
}
