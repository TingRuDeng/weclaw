package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ReadCodexQuota 通过 Codex app-server 查询当前账号额度。
func (a *ACPAgent) ReadCodexQuota(ctx context.Context) (CodexQuota, error) {
	if a.protocol != protocolCodexAppServer {
		return CodexQuota{}, fmt.Errorf("当前 Agent 不支持 Codex 额度查询")
	}
	result, err := a.rpc(ctx, "account/rateLimits/read", json.RawMessage("null"))
	if err != nil {
		return CodexQuota{}, err
	}
	return parseCodexQuota(result)
}

// parseCodexQuota 解析 Codex app-server 的 rate limit 快照。
func parseCodexQuota(data json.RawMessage) (CodexQuota, error) {
	var payload codexQuotaPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return CodexQuota{}, fmt.Errorf("parse account/rateLimits/read result: %w", err)
	}
	limits := parseCodexQuotaBuckets(payload)
	if len(limits) == 0 {
		return CodexQuota{}, nil
	}
	return CodexQuota{Limits: limits}, nil
}

// parseCodexQuotaBuckets 优先使用多桶结构，缺失时回退到兼容单桶结构。
func parseCodexQuotaBuckets(payload codexQuotaPayload) []CodexRateLimit {
	if len(payload.RateLimitsByLimitID) > 0 {
		return parseCodexQuotaMap(payload.RateLimitsByLimitID)
	}
	if payload.RateLimits == nil {
		return nil
	}
	limit := convertCodexRateLimit(*payload.RateLimits)
	return []CodexRateLimit{limit}
}

// parseCodexQuotaMap 稳定排序 limit_id，避免微信输出顺序抖动。
func parseCodexQuotaMap(items map[string]codexRateLimitSnapshot) []CodexRateLimit {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	limits := make([]CodexRateLimit, 0, len(keys))
	for _, key := range keys {
		limit := convertCodexRateLimit(items[key])
		if strings.TrimSpace(limit.ID) == "" {
			limit.ID = key
		}
		limits = append(limits, limit)
	}
	return limits
}

// convertCodexRateLimit 只提取微信展示需要的稳定字段。
func convertCodexRateLimit(snapshot codexRateLimitSnapshot) CodexRateLimit {
	return CodexRateLimit{
		ID:          snapshot.LimitID,
		Name:        snapshot.LimitName,
		PlanType:    snapshot.PlanType,
		ReachedType: snapshot.RateLimitReachedType,
		Primary:     convertCodexRateLimitWindow(snapshot.Primary),
		Secondary:   convertCodexRateLimitWindow(snapshot.Secondary),
		Credits:     convertCodexCredits(snapshot.Credits),
	}
}

// convertCodexRateLimitWindow 保留 nil，避免把缺失窗口误报为 0%。
func convertCodexRateLimitWindow(window *codexRateLimitWindow) *CodexRateLimitWindow {
	if window == nil {
		return nil
	}
	return &CodexRateLimitWindow{
		UsedPercent:        window.UsedPercent,
		ResetsAt:           window.ResetsAt,
		WindowDurationMins: window.WindowDurationMins,
	}
}

// convertCodexCredits 保留 nil，避免把缺失余额字段误报为无额度。
func convertCodexCredits(credits *codexCreditsSnapshot) *CodexCredits {
	if credits == nil {
		return nil
	}
	return &CodexCredits{
		Balance:    credits.Balance,
		HasCredits: credits.HasCredits,
		Unlimited:  credits.Unlimited,
	}
}

type codexQuotaPayload struct {
	RateLimits          *codexRateLimitSnapshot           `json:"rateLimits"`
	RateLimitsByLimitID map[string]codexRateLimitSnapshot `json:"rateLimitsByLimitId"`
}

type codexRateLimitSnapshot struct {
	Credits              *codexCreditsSnapshot `json:"credits"`
	LimitID              string                `json:"limitId"`
	LimitName            string                `json:"limitName"`
	PlanType             string                `json:"planType"`
	Primary              *codexRateLimitWindow `json:"primary"`
	RateLimitReachedType string                `json:"rateLimitReachedType"`
	Secondary            *codexRateLimitWindow `json:"secondary"`
}

type codexRateLimitWindow struct {
	ResetsAt           *int64 `json:"resetsAt"`
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
}

type codexCreditsSnapshot struct {
	Balance    string `json:"balance"`
	HasCredits bool   `json:"hasCredits"`
	Unlimited  bool   `json:"unlimited"`
}
