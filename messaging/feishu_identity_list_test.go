package messaging

import (
	"context"
	"strings"
	"testing"
)

func TestFeishuIdentityCommandListShowsAuthorizedUsersWithoutRoles(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_admin_user", "user_admin", "on_admin_user"))
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_regular", "user_regular", "on_regular"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_admin_user"), reply)
	reply.waitTexts(t, 1)
	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_regular"), reply)
	reply.waitTexts(t, 2)
	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users list"), reply)

	texts := reply.waitTexts(t, 3)
	listReply := texts[len(texts)-1]
	if !strings.Contains(listReply, "on_admin_user") || !strings.Contains(listReply, "on_regular") {
		t.Fatalf("reply=%q, want both authorized users", listReply)
	}
	if strings.Contains(listReply, "用户类型") || strings.Contains(listReply, "管理员") || strings.Contains(listReply, "普通用户") {
		t.Fatalf("reply=%q, must not expose removed role distinction", listReply)
	}
}
