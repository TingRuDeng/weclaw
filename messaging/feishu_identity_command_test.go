package messaging

import (
	"context"
	"path/filepath"
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

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users pending"), reply)

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

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_same_person --bot main"), reply)
	reply.waitTexts(t, 1)

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users list"), reply)

	texts := reply.waitTexts(t, 2)
	listReply := texts[len(texts)-1]
	if !strings.Contains(listReply, "已授权机器人") {
		t.Fatalf("reply=%q, want authorized scope", listReply)
	}
	if strings.Contains(listReply, "待授权机器人") ||
		strings.Contains(listReply, "下一步: /feishu users pending") ||
		strings.Contains(listReply, "部分授权") ||
		strings.Contains(listReply, "状态: 已授权") {
		t.Fatalf("reply=%q, list should not print pending scope", listReply)
	}

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users pending"), reply)

	texts = reply.waitTexts(t, 3)
	pendingReply := texts[len(texts)-1]
	if !strings.Contains(pendingReply, "暂无") || strings.Contains(pendingReply, "cli_b") {
		t.Fatalf("reply=%q, current bot must not expose another bot's pending scope", pendingReply)
	}
}

func TestFeishuIdentityCommandApprovesUnionIDForCurrentBotOnly(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_same_person"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "已授权") || !strings.Contains(texts[0], "on_same_person") {
		t.Fatalf("reply=%q, want approve confirmation", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if !testStringSliceContains(bots[0].AllowedUsers, "on_same_person") {
		t.Fatalf("current bot allowed=%#v, want union_id", bots[0].AllowedUsers)
	}
	if testStringSliceContains(bots[1].AllowedUsers, "on_same_person") {
		t.Fatalf("other bot allowed=%#v, remote command must stay account-scoped", bots[1].AllowedUsers)
	}
}

func TestFeishuIdentityCommandRejectsOtherBotTarget(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_same_person --bot android"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "只能管理当前机器人") {
		t.Fatalf("reply=%q, want account-scope rejection", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, bot := range cfg.Platforms["feishu"].Bots {
		if testStringSliceContains(bot.AllowedUsers, "on_same_person") {
			t.Fatalf("bot=%s allowed=%#v, rejected cross-bot command mutated config", bot.Name, bot.AllowedUsers)
		}
	}
}

func TestFeishuIdentityCommandCannotApproveIdentitySeenOnlyByOtherBot(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_b", "ou_b", "user_b", "on_other_bot"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_other_bot"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "未在当前机器人发现") {
		t.Fatalf("reply=%q, want current-account identity rejection", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if testStringSliceContains(cfg.Platforms["feishu"].Bots[0].AllowedUsers, "on_other_bot") {
		t.Fatal("identity seen only by another bot was authorized on current bot")
	}
}

func TestFeishuIdentityCommandCannotRevokeOtherBotAuthorization(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	bots := cfg.Platforms["feishu"].Bots
	bots[1].AllowedUsers = []string{"on_other_bot"}
	feishuCfg := cfg.Platforms["feishu"]
	feishuCfg.Bots = bots
	cfg.Platforms["feishu"] = feishuCfg
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_b", "ou_b", "user_b", "on_other_bot"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users revoke on_other_bot"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "未在当前机器人发现") {
		t.Fatalf("reply=%q, want current-account identity rejection", texts[0])
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !testStringSliceContains(loaded.Platforms["feishu"].Bots[1].AllowedUsers, "on_other_bot") {
		t.Fatal("current bot command revoked another bot's allowlist")
	}
}

func TestFeishuIdentityCommandRejectsNumericApprovalSelector(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve 1"), reply)

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

func TestFeishuIdentityCommandRejectsLegacyAdminFlag(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_same_person --admin"), reply)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	for _, bot := range bots {
		if testStringSliceContains(bot.AllowedUsers, "on_same_person") {
			t.Fatalf("bot=%s allowed=%#v, rejected legacy flag must not mutate config", bot.Name, bot.AllowedUsers)
		}
	}
	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "未知参数: --admin") {
		t.Fatalf("reply=%q, want legacy flag rejection", texts[0])
	}
}

func TestFeishuIdentityCommandRevokesCurrentBotAuthorization(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve on_same_person"), reply)
	reply.waitTexts(t, 1)
	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users revoke on_same_person"), reply)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if testStringSliceContains(bots[0].AllowedUsers, "on_same_person") {
		t.Fatalf("main allowed=%#v, should remove current bot user", bots[0].AllowedUsers)
	}
	if testStringSliceContains(bots[1].AllowedUsers, "on_same_person") {
		t.Fatalf("android allowed=%#v, current bot command must not mutate other bot", bots[1].AllowedUsers)
	}
	texts := reply.waitTexts(t, 2)
	if !strings.Contains(texts[1], "已取消飞书用户授权") {
		t.Fatalf("reply=%q, want revoke confirmation", texts[1])
	}
}

func TestFeishuIdentityCommandRevocationUpdatesRegistryBeforeReply(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	if err := config.Update(func(cfg *config.Config) error {
		platformCfg := cfg.Platforms["feishu"]
		platformCfg.Bots[0].AllowedUsers = []string{"on_same_person"}
		cfg.Platforms["feishu"] = platformCfg
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &accessCapabilityTestPlatform{name: platform.PlatformFeishu, account: "cli_a"},
		Access:   platform.NewAccessControl([]string{"on_same_person"}),
	}})
	handler.SetPlatformRegistry(registry)
	if _, ok := registry.AuthorizeIncomingMessage(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person")); !ok {
		t.Fatal("fixture identity was not authorized before revoke")
	}
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(
		context.Background(), feishuAdminCommandMessage(t, "/feishu users revoke on_same_person"), reply,
	)
	reply.waitTexts(t, 1)

	if _, ok := registry.AuthorizeIncomingMessage(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person")); ok {
		t.Fatal("revoked identity remained authorized until the soft-reload tick")
	}
}

func TestLocalFeishuIdentityRevokeScopesOpenIDToSelectedBot(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	if err := config.Update(func(cfg *config.Config) error {
		platformCfg := cfg.Platforms["feishu"]
		platformCfg.Bots[0].AllowedUsers = []string{"ou_b"}
		cfg.Platforms["feishu"] = platformCfg
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "feishu-identities.json")
	store := newFeishuIdentityStore()
	store.SetFilePath(identityPath)
	store.Remember(feishuIdentityMessage("cli_a", "ou_a", "user_same", "on_same"))
	store.Remember(feishuIdentityMessage("cli_b", "ou_b", "user_same", "on_same"))

	if _, err := RevokeFeishuIdentity(FeishuIdentityRevokeRequest{
		Selector: "on_same", BotRef: "cli_a", FilePath: identityPath,
	}); err == nil || !strings.Contains(err.Error(), "未找到该飞书用户授权") {
		t.Fatalf("revoke error=%v, want selected bot to ignore another bot open_id", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !testStringSliceContains(loaded.Platforms["feishu"].Bots[0].AllowedUsers, "ou_b") {
		t.Fatal("cli_a revoke removed cli_b open_id alias from the selected bot")
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

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve-code "+record.AuthCode+" --name 张三"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "张三 (on_same_person)") {
		t.Fatalf("reply=%q, want display name in approval", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if !testStringSliceContains(bots[0].AllowedUsers, "on_same_person") {
		t.Fatalf("current bot allowed=%#v, want union_id", bots[0].AllowedUsers)
	}
	if testStringSliceContains(bots[1].AllowedUsers, "on_same_person") {
		t.Fatalf("other bot allowed=%#v, code approval must stay account-scoped", bots[1].AllowedUsers)
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

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users pending"), reply)

	texts := reply.waitTexts(t, 1)
	if strings.Contains(texts[0], "授权码: 123456") ||
		strings.Contains(texts[0], "approve-code 123456") {
		t.Fatalf("reply=%q, should hide expired auth code", texts[0])
	}
}

func TestFeishuIdentityCommandAllowsObservedIdentityWithoutUnionID(t *testing.T) {
	setupFeishuIdentityCommandConfig(t)
	handler := newFeishuIdentityCommandHandler(t)
	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", ""))
	reply := newAdminCommandTestReplier()

	handler.HandleMessage(context.Background(), feishuAdminCommandMessage(t, "/feishu users approve ou_a"), reply)

	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "已授权飞书用户: user_a") {
		t.Fatalf("reply=%q, want observed user_id approval", texts[0])
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !testStringSliceContains(cfg.Platforms["feishu"].Bots[0].AllowedUsers, "user_a") {
		t.Fatalf("current bot allowed=%#v, want observed user_id", cfg.Platforms["feishu"].Bots[0].AllowedUsers)
	}
}

func setupFeishuIdentityCommandConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	enabled := true
	cfg := config.DefaultConfig()
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
	return handler
}

func feishuAdminCommandMessage(t *testing.T, text string) platform.IncomingMessage {
	t.Helper()
	return authorizeIncomingMessageForTest(t, platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "ou_admin",
		Metadata:  map[string]string{"feishu_union_id": "on_admin"},
		MessageID: strings.ReplaceAll(text, " ", "-"),
		Text:      text,
	}, "on_admin")
}

func testStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
