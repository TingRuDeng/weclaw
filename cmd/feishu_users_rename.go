package cmd

import (
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

type feishuUsersRenameOptions struct {
	Selector    string
	DisplayName string
}

// validateFeishuUsersRenameArgs 校验显示名补全命令的两个必要参数。
func validateFeishuUsersRenameArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 2 && strings.TrimSpace(args[0]) != "" && strings.TrimSpace(args[1]) != "" {
		return nil
	}
	return fmt.Errorf("用法: weclaw feishu users rename <union_id|user_id|open_id> <显示名>")
}

// runFeishuUsersRename 为已发现飞书身份写入本地显示名。
func runFeishuUsersRename(opts feishuUsersRenameOptions) error {
	result, err := messaging.RenameFeishuIdentity(messaging.FeishuIdentityRenameRequest{
		Selector:    opts.Selector,
		DisplayName: opts.DisplayName,
	})
	if err != nil {
		return err
	}
	fmt.Println(renderFeishuIdentityRenameResult(result))
	return nil
}

// renderFeishuIdentityRenameResult 输出显示名更新结果，并保留稳定身份。
func renderFeishuIdentityRenameResult(result messaging.FeishuIdentityRenameResult) string {
	return fmt.Sprintf("已更新飞书用户显示名: %s (%s)", result.DisplayName, result.Identity)
}
