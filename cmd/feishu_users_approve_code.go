package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

type feishuUsersApproveCodeOptions struct {
	Code        string
	BotRef      string
	Admin       bool
	DisplayName string
}

// validateFeishuUsersApproveCodeArgs 校验授权码命令必须带授权码。
func validateFeishuUsersApproveCodeArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		return nil
	}
	return fmt.Errorf("用法: weclaw feishu users approve-code <授权码> [--bot <name|app_id>] [--admin] [--name <显示名>]")
}

// runFeishuUsersApproveCode 使用短期授权码完成用户授权。
func runFeishuUsersApproveCode(opts feishuUsersApproveCodeOptions) error {
	result, err := messaging.ApproveFeishuIdentityByCode(messaging.FeishuIdentityApproveCodeRequest{
		Code:        opts.Code,
		BotRef:      opts.BotRef,
		Admin:       opts.Admin,
		DisplayName: opts.DisplayName,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.DisplayName) == "" {
		result.DisplayName = feishuDisplayNameForApprovedIdentity(result.Identity)
	}
	fmt.Println(messaging.RenderFeishuIdentityApproval(result))
	return nil
}

func feishuDisplayNameForApprovedIdentity(identity string) string {
	views, err := messaging.LoadFeishuIdentityViews("", false)
	if err != nil {
		return ""
	}
	view, ok := feishuIdentityViewByIdentity(views, identity)
	if !ok {
		return ""
	}
	if view.DisplayName != "" {
		return view.DisplayName
	}
	_, accounts := feishuBotUserListMetadata()
	result := lookupFeishuIdentityNames(context.Background(), []messaging.FeishuIdentityView{view}, accounts)
	displayName := feishuIdentityResolvedName(view, result.Names)
	if strings.TrimSpace(displayName) != "" {
		_, _ = messaging.RenameFeishuIdentity(messaging.FeishuIdentityRenameRequest{
			Selector:    identity,
			DisplayName: displayName,
		})
	}
	return displayName
}

func feishuIdentityViewByIdentity(views []messaging.FeishuIdentityView, identity string) (messaging.FeishuIdentityView, bool) {
	identity = strings.TrimSpace(identity)
	for _, view := range views {
		if view.Key == identity || view.UnionID == identity || view.UserID == identity || view.OpenID == identity {
			return view, true
		}
	}
	return messaging.FeishuIdentityView{}, false
}
