package messaging

import (
	"strings"
)

// RenderFeishuIdentityApproval 渲染授权完成结果，供 CLI 和飞书内管理命令复用。
func RenderFeishuIdentityApproval(result FeishuIdentityApproveResult) string {
	lines := []string{"已授权飞书用户: " + feishuApprovalIdentityLabel(result)}
	if len(result.Bots) > 0 {
		lines = append(lines, "已写入机器人: "+strings.Join(result.Bots, ", "))
	}
	if result.Admin {
		lines = append(lines, "已同步加入 admin_users")
	}
	return strings.Join(lines, "\n")
}

func feishuApprovalIdentityLabel(result FeishuIdentityApproveResult) string {
	name := strings.TrimSpace(result.DisplayName)
	identity := strings.TrimSpace(result.Identity)
	if name == "" || name == identity {
		return identity
	}
	return name + " (" + identity + ")"
}
