package messaging

import (
	"fmt"
	"strings"
)

// RenderFeishuIdentityApproval 渲染飞书身份授权后的用户可见结果。
func RenderFeishuIdentityApproval(result FeishuIdentityApproveResult) string {
	lines := []string{
		"已授权飞书用户: " + renderFeishuApprovedIdentity(result),
		"机器人: " + strings.Join(result.Bots, ", "),
		"配置已写入，运行中服务会通过配置热重载生效。",
	}
	if result.Admin {
		lines = append(lines, "已同步加入 admin_users。")
	}
	return strings.Join(lines, "\n")
}

func renderFeishuApprovedIdentity(result FeishuIdentityApproveResult) string {
	if strings.TrimSpace(result.DisplayName) == "" {
		return result.Identity
	}
	return fmt.Sprintf("%s (%s)", result.DisplayName, result.Identity)
}
