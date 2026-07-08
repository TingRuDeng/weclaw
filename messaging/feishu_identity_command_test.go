package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestFeishuIdentityCommandListsPendingUsers(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users pending"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "on_same_person") || !strings.Contains(texts[0], "cli_a") {
		t.Fatalf("reply=%q, want pending identity and account", texts[0])
	}
}

func TestFeishuIdentityCommandListHidesPendingScope(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_b", "ou_b", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users approve on_same_person --bot main"), reply)
	reply.waitTexts(t, 1)

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users list"), reply)

	texts := reply.waitTexts(t, 2)
	listReply := texts[len(texts)-1]
	if !strings.Contains(listReply, "已授权机器人") || !strings.Contains(listReply, "状态: 已授权") {
		t.Fatalf("reply=%q, want authorized scope", listReply)
	}
	if strings.Contains(listReply, "待授权机器人") ||
		strings.Contains(listReply, "下一步: /feishu users pending") ||
		strings.Contains(listReply, "部分授权") {
		t.Fatalf("reply=%q, list should not print pending scope", listReply)
	}

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users pending"), reply)

	texts = reply.waitTexts(t, 3)
	pendingReply := texts[len(texts)-1]
	if !strings.Contains(pendingReply, "待授权机器人") ||
		!strings.Contains(pendingReply, "cli_b") ||
		!strings.Contains(pendingReply, "状态: 待授权") ||
		!strings.Contains(pendingReply, "授权命令: /feishu users approve on_same_person") ||
		!strings.Contains(pendingReply, "授权说明: 执行上面的授权命令可授权该用户访问待授权机器人。") {
		t.Fatalf("reply=%q, want pending scope in pending command", pendingReply)
	}
	if strings.Contains(pendingReply, "已授权机器人") {
		t.Fatalf("reply=%q, pending should not print authorized scope", pendingReply)
	}
}

func TestFeishuIdentityCommandApprovesUnionIDForAllBots(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users approve on_same_person"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "已授权") || !strings.Contains(texts[0], "on_same_person") {
		t.Fatalf("reply=%q, want approve confirmation", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if !testStringSliceContains(bot.AllowedUsers, "on_same_person") {
			t.Fatalf("bot=%s allowed=%#v, want union_id", bot.Name, bot.AllowedUsers)
		}
	}
}

func TestFeishuIdentityCommandRejectsNumericApprovalSelector(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users approve 1"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "请使用 union_id、user_id 或 open_id") {
		t.Fatalf("reply=%q, want stable selector warning", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if testStringSliceContains(bot.AllowedUsers, "on_same_person") {
			t.Fatalf("bot=%s allowed=%#v, should not approve numeric selector", bot.Name, bot.AllowedUsers)
		}
	}
}

func TestFeishuIdentityCommandApprovesSingleBotAndAdminUser(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users approve on_same_person --bot android --admin"), reply)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if testStringSliceContains(bots[0].AllowedUsers, "on_same_person") {
		t.Fatalf("main allowed=%#v, should not authorize single-bot approval", bots[0].AllowedUsers)
	}
	if !testStringSliceContains(bots[1].AllowedUsers, "on_same_person") {
		t.Fatalf("android allowed=%#v, want union_id", bots[1].AllowedUsers)
	}
	if !testStringSliceContains(cfg.AdminUsers, "on_same_person") {
		t.Fatalf("admin users=%#v, want union_id", cfg.AdminUsers)
	}
	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "已同步加入 admin_users") {
		t.Fatalf("reply=%q, want admin confirmation", texts[0])
	}
}

func TestFeishuIdentityCommandApprovesByCodeWithDisplayName(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	record, ok := handler.ensureFeishuIdentities().IssueAuthCode("on_same_person", time.Now().UTC())
	if !ok {
		t.Fatal("IssueAuthCode ok=false, want true")
	}
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users approve-code "+record.AuthCode+" --name 张三"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "张三 (on_same_person)") {
		t.Fatalf("reply=%q, want display name in approval", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if !testStringSliceContains(bot.AllowedUsers, "on_same_person") {
			t.Fatalf("bot=%s allowed=%#v, want union_id", bot.Name, bot.AllowedUsers)
		}
	}
}

func TestFeishuIdentityCommandPendingHidesExpiredAuthCode(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	store := handler.ensureFeishuIdentities()
	store.records["on_same_person"] = feishuIdentityRecord{
		Key:               "on_same_person",
		UnionID:           "on_same_person",
		OpenID:            "ou_a",
		Accounts:          []string{"cli_a"},
		AuthCode:          "123456",
		AuthCodeExpiresAt: "2000-01-01T00:00:00Z",
		Pending:           true,
	}
	store.save()
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users pending"), reply)

	texts := reply.waitTexts(t, 1)
	if strings.Contains(texts[0], "授权码: 123456") ||
		strings.Contains(texts[0], "approve-code 123456") {
		t.Fatalf("reply=%q, should hide expired auth code", texts[0])
	}
}

func TestFeishuIdentityCommandAdminRequiresUnionID(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", ""))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage("/feishu users approve ou_a --admin"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "缺少 union_id") {
		t.Fatalf("reply=%q, want union_id required warning", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if testStringSliceContains(cfg.AdminUsers, "ou_a") || testStringSliceContains(cfg.AdminUsers, "user_a") {
		t.Fatalf("admin users=%#v, should not write open_id or user_id", cfg.AdminUsers)
	}
}

func setupFeishuIdentityCommandConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	enabled := true
	cfg := config.DefaultConfig()
	cfg.AdminUsers = []string{"on_admin"}
	cfg.Platforms["feishu"] = config.PlatformConfig{
		Enabled: &enabled,
		Bots: []config.FeishuBotConfig{
			{Name: "main", AppID: "cli_a"},
			{Name: "android", AppID: "cli_b"},
		},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func newFeishuIdentityCommandHandler(t *testing.T) *Handler {
	t.Helper()
	handler := NewHandler(nil, nil)
	handler.SetFeishuIdentityFile(DefaultFeishuIdentityFile())
	handler.SetAdminUsers([]string{"on_admin"})
	return handler
}

func feishuAdminCommandMessage(text string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "ou_admin",
		Metadata:  map[string]string{"feishu_union_id": "on_admin"},
		MessageID: strings.ReplaceAll(text, " ", "-"),
		Text:      text,
	}
}

func testStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
