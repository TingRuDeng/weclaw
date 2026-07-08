package messaging

import (
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
)

type feishuIdentityAuthorization struct {
	AuthorizedAccounts   []string
	UnauthorizedAccounts []string
}

func feishuIdentityAuthorizationForRecord(record feishuIdentityRecord, cfg config.Config, cfgOK bool) feishuIdentityAuthorization {
	if !cfgOK {
		return fallbackFeishuIdentityAuthorization(record)
	}
	bots := cfg.Platforms["feishu"].Bots
	if len(bots) == 0 {
		return fallbackFeishuIdentityAuthorization(record)
	}
	return classifyFeishuIdentityAccounts(record, bots)
}

func fallbackFeishuIdentityAuthorization(record feishuIdentityRecord) feishuIdentityAuthorization {
	auth := feishuIdentityAuthorization{}
	if record.Approved {
		auth.AuthorizedAccounts = append([]string(nil), record.Accounts...)
	}
	if record.Pending && !record.Approved {
		auth.UnauthorizedAccounts = append([]string(nil), record.Accounts...)
	}
	return auth
}

func classifyFeishuIdentityAccounts(record feishuIdentityRecord, bots []config.FeishuBotConfig) feishuIdentityAuthorization {
	auth := feishuIdentityAuthorization{}
	seenAccounts := feishuIdentitySeenAccounts(record, bots)
	for _, accountID := range seenAccounts {
		bot, ok := feishuBotByAppID(bots, accountID)
		if ok && feishuIdentityAllowedByBot(record, accountID, bot) {
			auth.AuthorizedAccounts = append(auth.AuthorizedAccounts, accountID)
			continue
		}
		auth.UnauthorizedAccounts = append(auth.UnauthorizedAccounts, accountID)
	}
	return auth
}

func feishuIdentitySeenAccounts(record feishuIdentityRecord, bots []config.FeishuBotConfig) []string {
	accounts := append([]string(nil), record.Accounts...)
	for _, bot := range bots {
		appID := strings.TrimSpace(bot.AppID)
		if appID == "" || stringSliceContains(accounts, appID) {
			continue
		}
		if feishuIdentityAllowedByBot(record, appID, bot) {
			accounts = append(accounts, appID)
		}
	}
	return accounts
}

func feishuBotByAppID(bots []config.FeishuBotConfig, appID string) (config.FeishuBotConfig, bool) {
	appID = strings.TrimSpace(appID)
	for _, bot := range bots {
		if strings.TrimSpace(bot.AppID) == appID {
			return bot, true
		}
	}
	return config.FeishuBotConfig{}, false
}

func feishuIdentityAllowedByBot(record feishuIdentityRecord, accountID string, bot config.FeishuBotConfig) bool {
	for _, key := range feishuIdentityAuthKeys(record, accountID) {
		if stringSliceContains(bot.AllowedUsers, key) {
			return true
		}
	}
	return false
}

func feishuIdentityAuthKeys(record feishuIdentityRecord, accountID string) []string {
	keys := []string{record.Key, record.UnionID, record.UserID, record.OpenID}
	if accountOpenID := strings.TrimSpace(record.OpenIDs[accountID]); accountOpenID != "" {
		keys = append(keys, accountOpenID)
	}
	for _, openID := range record.OpenIDs {
		keys = append(keys, openID)
	}
	return uniqueTrimmedStrings(keys)
}

func uniqueTrimmedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
