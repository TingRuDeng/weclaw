package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
)

// printFeishuIdentityNameWarnings 把可恢复的通讯录查询失败压缩为手动补全提示。
func printFeishuIdentityNameWarnings(warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Println("提示: 部分姓名未能从飞书通讯录获取，可使用 weclaw feishu users rename <id> <显示名> 手动补全。")
}

// feishuIdentityDisplayLabel 优先显示手动备注，其次显示通讯录姓名。
func feishuIdentityDisplayLabel(view messaging.FeishuIdentityView, names map[string]string) string {
	name := firstNonBlankUserDisplayName(view.DisplayName, feishuIdentityResolvedName(view, names))
	return feishuIdentityDisplayLabelForNames(view.Key, name)
}

// feishuIdentityDisplayLabelForNames 保留稳定 ID，避免展示名参与权限判断。
func feishuIdentityDisplayLabelForNames(key string, names ...string) string {
	name := firstNonBlankUserDisplayName(names...)
	if name == "" || name == key {
		return key
	}
	return fmt.Sprintf("%s (%s)", name, key)
}

// firstNonBlankUserDisplayName 选择第一个可展示的非空姓名。
func firstNonBlankUserDisplayName(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// viewHasManualFeishuDisplayName 判断用户是否已经手动维护显示名。
func viewHasManualFeishuDisplayName(view messaging.FeishuIdentityView) bool {
	return strings.TrimSpace(view.DisplayName) != ""
}

// filterFeishuViewsNeedingContactName 只对缺少手动显示名的记录查询通讯录。
func filterFeishuViewsNeedingContactName(views []messaging.FeishuIdentityView) []messaging.FeishuIdentityView {
	out := make([]messaging.FeishuIdentityView, 0, len(views))
	for _, view := range views {
		if !viewHasManualFeishuDisplayName(view) {
			out = append(out, view)
		}
	}
	return out
}

// lookupFeishuIdentityNamesForViews 把通讯录姓名查询限定为可选补全项。
func lookupFeishuIdentityNamesForViews(ctx context.Context, views []messaging.FeishuIdentityView, accounts []feishuIdentityNameLookupAccount) feishuIdentityNameLookupResult {
	views = filterFeishuViewsNeedingContactName(views)
	if len(views) == 0 || len(accounts) == 0 {
		return feishuIdentityNameLookupResult{}
	}
	return lookupFeishuIdentityNames(ctx, views, accounts)
}
