package cmd

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

// checkPlatforms 汇总已启用平台的凭证与访问控制检查。
func checkPlatforms(cfg *config.Config, deps doctorDeps) []doctorResult {
	var results []doctorResult
	wechatEnabled, feishuEnabled := platformEnablement(cfg)
	if wechatEnabled {
		results = append(results, checkWeChat(deps))
		results = append(results, checkAllowlist(cfg, string(platform.PlatformWeChat)))
	}
	if feishuEnabled {
		results = append(results, checkFeishuBots(cfg, deps)...)
	}
	if !wechatEnabled && !feishuEnabled {
		results = append(results, doctorResult{Name: "platforms", Status: doctorWarn, Detail: "no platform enabled; nothing to run"})
	}
	return results
}

// platformEnablement 解析启用的平台，必须与启动阶段保持一致，避免 doctor 误报实际未启动的平台。
func platformEnablement(cfg *config.Config) (wechat bool, feishu bool) {
	wechat = wechatEnabled(cfg)
	feishuCfg := cfg.Platforms[string(platform.PlatformFeishu)]
	feishu = feishuCfg.Enabled != nil && *feishuCfg.Enabled
	return wechat, feishu
}

func checkWeChat(deps doctorDeps) doctorResult {
	result := doctorResult{Name: "platform wechat"}
	count, err := deps.wechatAccounts()
	if err != nil {
		result.Status = doctorFail
		result.Detail = "load credentials: " + err.Error()
		return result
	}
	if count == 0 {
		result.Status = doctorFail
		result.Detail = "no WeChat account; run `weclaw wechat login`"
		return result
	}
	result.Status = doctorOK
	result.Detail = fmt.Sprintf("%d account(s)", count)
	return result
}

func checkFeishuBots(cfg *config.Config, deps doctorDeps) []doctorResult {
	bots := cfg.Platforms[string(platform.PlatformFeishu)].Bots
	if len(bots) == 0 {
		return []doctorResult{{Name: "platform feishu", Status: doctorFail, Detail: "platforms.feishu.bots is required"}}
	}
	results := make([]doctorResult, 0, len(bots)*2)
	for _, bot := range bots {
		results = append(results, checkFeishuBot(bot, deps), checkFeishuBotAllowlist(bot))
	}
	return results
}

func checkFeishuBot(bot config.FeishuBotConfig, deps doctorDeps) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("platform feishu %s", feishuBotDisplayLabel(bot))}
	if err := deps.feishuCredsOK(bot.Name); err != nil {
		result.Status = doctorFail
		result.Detail = err.Error()
		return result
	}
	result.Status = doctorOK
	result.Detail = "credentials present"
	return result
}

func checkFeishuBotAllowlist(bot config.FeishuBotConfig) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("access control feishu %s", feishuBotDisplayLabel(bot))}
	if len(bot.AllowedUsers) == 0 {
		result.Status = doctorWarn
		result.Detail = "empty allowed_users -> default-deny rejects everyone; add allowed_users"
		return result
	}
	result.Status = doctorOK
	result.Detail = fmt.Sprintf("%d allowed user(s)", len(bot.AllowedUsers))
	return result
}

func checkAllowlist(cfg *config.Config, name string) doctorResult {
	result := doctorResult{Name: fmt.Sprintf("access control %s", name)}
	pc, ok := cfg.Platforms[name]
	if !ok || len(pc.AllowedUsers) == 0 {
		result.Status = doctorWarn
		result.Detail = "empty allow_users → default-deny rejects everyone; add allowed_users"
		return result
	}
	result.Status = doctorOK
	result.Detail = fmt.Sprintf("%d allowed user(s)", len(pc.AllowedUsers))
	return result
}

func checkAPIToken(cfg *config.Config) doctorResult {
	result := doctorResult{Name: "api server"}
	addr := strings.TrimSpace(cfg.APIAddr)
	if addr == "" || isLoopbackAddr(addr) {
		result.Status = doctorOK
		result.Detail = "loopback or default address"
		return result
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		result.Status = doctorFail
		result.Detail = fmt.Sprintf("api_addr %q is non-loopback but api_token is empty", addr)
		return result
	}
	result.Status = doctorOK
	result.Detail = "token configured for non-loopback address"
	return result
}

func isLoopbackAddr(addr string) bool {
	host := addr
	if parsed, _, err := net.SplitHostPort(addr); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
