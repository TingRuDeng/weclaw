package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	feishuplatform "github.com/fastclaw-ai/weclaw/feishu"
	"github.com/fastclaw-ai/weclaw/messaging"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	contact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
)

const feishuContactLookupTimeout = 10 * time.Second

type feishuIdentityNameLookupAccount struct {
	Name  string
	AppID string
	Label string
}

type feishuIdentityNameLookupResult struct {
	Names    map[string]string
	Warnings []string
}

type feishuContactUserQuery struct {
	BotName    string
	BotLabel   string
	UserID     string
	UserIDType string
}

var (
	lookupFeishuIdentityNames = lookupFeishuIdentityNamesFromContact
	fetchFeishuContactNameFn  = fetchFeishuContactName
)

// lookupFeishuIdentityNamesFromContact 用已配置机器人凭证补全身份记录的人类可读姓名。
func lookupFeishuIdentityNamesFromContact(ctx context.Context, views []messaging.FeishuIdentityView, accounts []feishuIdentityNameLookupAccount) feishuIdentityNameLookupResult {
	if len(views) == 0 || len(accounts) == 0 {
		return feishuIdentityNameLookupResult{}
	}
	ctx, cancel := context.WithTimeout(ctx, feishuContactLookupTimeout)
	defer cancel()

	result := feishuIdentityNameLookupResult{Names: make(map[string]string)}
	for _, view := range views {
		name, warnings := lookupFeishuIdentityName(ctx, view, accounts)
		result.Warnings = append(result.Warnings, warnings...)
		if name != "" {
			rememberFeishuIdentityName(result.Names, view, name)
		}
	}
	return result
}

// lookupFeishuIdentityName 逐个尝试可用机器人，避免某个机器人缺权限时阻断其它查询路径。
func lookupFeishuIdentityName(ctx context.Context, view messaging.FeishuIdentityView, accounts []feishuIdentityNameLookupAccount) (string, []string) {
	warnings := []string{}
	for _, query := range feishuContactQueriesForView(view, accounts) {
		name, err := fetchFeishuContactNameFn(ctx, query)
		if err == nil {
			return name, nil
		}
		warnings = append(warnings, fmt.Sprintf("%s 查询 %s 失败: %v", query.BotLabel, view.Key, err))
	}
	return "", warnings
}

// fetchFeishuContactName 通过飞书通讯录用户接口读取单个用户姓名。
func fetchFeishuContactName(ctx context.Context, query feishuContactUserQuery) (string, error) {
	if query.BotName == "" {
		return "", fmt.Errorf("机器人未配置 name，无法读取凭证")
	}
	creds, err := feishuplatform.LoadCredentialsForBot(query.BotName)
	if err != nil {
		return "", err
	}
	client := lark.NewClient(creds.AppID, creds.AppSecret)
	req := contact.NewGetUserReqBuilder().
		UserId(query.UserID).
		UserIdType(query.UserIDType).
		Build()
	resp, err := client.Contact.User.Get(ctx, req)
	if err != nil {
		return "", err
	}
	return feishuContactNameFromResponse(resp)
}

// feishuContactNameFromResponse 从 SDK 响应中选择最适合展示的姓名字段。
func feishuContactNameFromResponse(resp *contact.GetUserResp) (string, error) {
	if resp == nil {
		return "", fmt.Errorf("飞书接口未返回响应")
	}
	if !resp.Success() {
		return "", fmt.Errorf("飞书接口返回 code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.User == nil {
		return "", fmt.Errorf("飞书接口未返回用户")
	}
	name := firstNonBlankString(
		stringPtrValue(resp.Data.User.Name),
		stringPtrValue(resp.Data.User.Nickname),
		stringPtrValue(resp.Data.User.EnName),
	)
	if name == "" {
		return "", fmt.Errorf("飞书接口未返回姓名")
	}
	return name, nil
}

// feishuContactQueriesForView 优先使用应用内 open_id，缺失时再用 union_id 兜底查询。
func feishuContactQueriesForView(view messaging.FeishuIdentityView, accounts []feishuIdentityNameLookupAccount) []feishuContactUserQuery {
	queries := []feishuContactUserQuery{}
	for _, accountID := range view.Accounts {
		account, ok := feishuLookupAccountByAppID(accounts, accountID)
		if !ok {
			continue
		}
		if openID := feishuOpenIDForLookup(view, account.AppID); openID != "" {
			queries = append(queries, newFeishuContactUserQuery(account, openID, contact.UserIdTypeOpenId))
		}
	}
	if len(queries) == 0 && strings.TrimSpace(view.UnionID) != "" {
		queries = append(queries, feishuUnionIDQueries(view.UnionID, accounts)...)
	}
	return queries
}

// feishuOpenIDForLookup 兼容旧记录：没有 open_ids 时使用顶层 open_id。
func feishuOpenIDForLookup(view messaging.FeishuIdentityView, appID string) string {
	if openID := strings.TrimSpace(view.OpenIDs[appID]); openID != "" {
		return openID
	}
	if len(view.OpenIDs) == 0 && len(view.Accounts) == 1 {
		return strings.TrimSpace(view.OpenID)
	}
	return ""
}

// feishuUnionIDQueries 为没有应用 open_id 的旧记录构造 union_id 查询。
func feishuUnionIDQueries(unionID string, accounts []feishuIdentityNameLookupAccount) []feishuContactUserQuery {
	queries := make([]feishuContactUserQuery, 0, len(accounts))
	for _, account := range accounts {
		queries = append(queries, newFeishuContactUserQuery(account, unionID, contact.UserIdTypeUnionId))
	}
	return queries
}

// newFeishuContactUserQuery 保持查询参数创建入口一致，避免调用处拼错 ID 类型。
func newFeishuContactUserQuery(account feishuIdentityNameLookupAccount, userID, userIDType string) feishuContactUserQuery {
	return feishuContactUserQuery{
		BotName:    account.Name,
		BotLabel:   account.Label,
		UserID:     strings.TrimSpace(userID),
		UserIDType: userIDType,
	}
}

// feishuLookupAccountByAppID 把身份记录里的 app_id 映射到本地 bot 配置。
func feishuLookupAccountByAppID(accounts []feishuIdentityNameLookupAccount, appID string) (feishuIdentityNameLookupAccount, bool) {
	appID = strings.TrimSpace(appID)
	for _, account := range accounts {
		if account.AppID == appID {
			return account, true
		}
	}
	return feishuIdentityNameLookupAccount{}, false
}

// rememberFeishuIdentityName 把同一个人的多个 ID 都映射到同一个展示名。
func rememberFeishuIdentityName(names map[string]string, view messaging.FeishuIdentityView, name string) {
	name = strings.TrimSpace(name)
	for _, key := range feishuIdentityNameKeys(view) {
		names[key] = name
	}
}

// feishuIdentityResolvedName 按稳定 ID、用户 ID、open_id 的顺序查找展示名。
func feishuIdentityResolvedName(view messaging.FeishuIdentityView, names map[string]string) string {
	for _, key := range feishuIdentityNameKeys(view) {
		if name := strings.TrimSpace(names[key]); name != "" {
			return name
		}
	}
	return ""
}

// feishuIdentityNameKeys 返回同一身份的全部可匹配 ID。
func feishuIdentityNameKeys(view messaging.FeishuIdentityView) []string {
	keys := []string{view.Key, view.UnionID, view.UserID, view.OpenID}
	for _, openID := range view.OpenIDs {
		keys = append(keys, openID)
	}
	return compactUniqueStrings(keys)
}

// compactUniqueStrings 清理空值并保持首次出现顺序。
func compactUniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

// firstNonBlankString 选择第一个非空展示字段。
func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// stringPtrValue 安全读取 SDK 返回的字符串指针。
func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
