package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

// renderCodexQuota 查询并渲染 Codex 账号额度。
func (h *Handler) renderCodexQuota(ctx context.Context, ag agent.Agent) string {
	quotaAg, ok := ag.(agent.CodexQuotaAgent)
	if !ok {
		return "当前 Codex Agent 不支持额度查询。"
	}
	quota, err := quotaAg.ReadCodexQuota(ctx)
	if err != nil {
		return fmt.Sprintf("查询 Codex 额度失败: %v", err)
	}
	if len(quota.Limits) == 0 {
		return "Codex 当前没有返回额度信息。"
	}
	return renderCodexQuotaText(quota)
}

// renderCodexQuotaText 将额度桶转换成微信可读文本。
func renderCodexQuotaText(quota agent.CodexQuota) string {
	lines := []string{"Codex 账号额度:"}
	for _, limit := range quota.Limits {
		lines = append(lines, codexQuotaLimitLabel(limit))
		lines = append(lines, codexQuotaMetadataLines(limit)...)
		lines = append(lines, codexQuotaWindowLine("primary", limit.Primary))
		lines = append(lines, codexQuotaWindowLine("secondary", limit.Secondary))
		if limit.Credits != nil {
			lines = append(lines, "credits: "+codexCreditsLabel(*limit.Credits))
		}
		if strings.TrimSpace(limit.ReachedType) != "" {
			lines = append(lines, "已达到限制: "+limit.ReachedType)
		}
	}
	return wechatCommandText(lines...)
}

// codexQuotaMetadataLines 只展示非空元信息，避免输出噪声。
func codexQuotaMetadataLines(limit agent.CodexRateLimit) []string {
	var lines []string
	if strings.TrimSpace(limit.PlanType) != "" {
		lines = append(lines, "plan: "+limit.PlanType)
	}
	return lines
}

// codexQuotaLimitLabel 在名称不同于 ID 时补充展示名称。
func codexQuotaLimitLabel(limit agent.CodexRateLimit) string {
	id := strings.TrimSpace(limit.ID)
	name := strings.TrimSpace(limit.Name)
	if id == "" {
		id = "(unknown)"
	}
	if name == "" || name == id {
		return id
	}
	return id + " (" + name + ")"
}

// codexQuotaWindowLine 展示单个限额窗口，缺失时明确标注未返回。
func codexQuotaWindowLine(name string, window *agent.CodexRateLimitWindow) string {
	if window == nil {
		return name + ": 未返回"
	}
	parts := []string{fmt.Sprintf("%s: 已用 %d%%", name, window.UsedPercent)}
	if window.WindowDurationMins != nil {
		parts = append(parts, fmt.Sprintf("窗口 %d 分钟", *window.WindowDurationMins))
	}
	if window.ResetsAt != nil {
		parts = append(parts, "重置 "+formatCodexResetTime(*window.ResetsAt))
	}
	return strings.Join(parts, "，")
}

// codexCreditsLabel 渲染余额字段，区分无限额度、有额度和无额度。
func codexCreditsLabel(credits agent.CodexCredits) string {
	if credits.Unlimited {
		return "无限额度"
	}
	status := "无额度"
	if credits.HasCredits {
		status = "有额度"
	}
	if strings.TrimSpace(credits.Balance) != "" {
		return status + "，余额 " + credits.Balance
	}
	return status
}

// formatCodexResetTime 兼容秒级和毫秒级 Unix 时间戳。
func formatCodexResetTime(value int64) string {
	if value > 1_000_000_000_000 {
		value = value / 1000
	}
	return time.Unix(value, 0).Local().Format("2006-01-02 15:04")
}
