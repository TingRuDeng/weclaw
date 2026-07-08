package messaging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestServiceAdminRestartPersistsCompletionNotice(t *testing.T) {
	path := useAdminRestartNotificationPath(t)
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	h.SetServiceAdminCommandExecutor(func(ctx context.Context, command string, args []string) (string, error) {
		return "restart scheduled", nil
	})
	reply := newAdminCommandTestReplier()

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		AccountID: "cli_a",
		UserID:    "ou_admin",
		ChatID:    "oc_chat",
		Text:      "/restart --force",
		Metadata:  map[string]string{"feishu_union_id": "on_admin"},
	}, reply)

	texts := reply.waitTexts(t, 2)
	if !strings.Contains(texts[1], "重启完成后会自动发送通知") {
		t.Fatalf("reply texts=%#v, want restart completion notification hint", texts)
	}
	notifications := readAdminRestartNotifications(t, path)
	if len(notifications) != 1 {
		t.Fatalf("notifications=%#v, want one pending restart notification", notifications)
	}
	got := notifications[0]
	if got.Platform != platform.PlatformFeishu || got.AccountID != "cli_a" || got.ChatID != "oc_chat" || got.UserID != "ou_admin" {
		t.Fatalf("notification=%#v, want original feishu route", got)
	}
}

func TestDeliverPendingRestartNotificationsSendsCompletionNotice(t *testing.T) {
	path := useAdminRestartNotificationPath(t)
	writeAdminRestartNotificationsForTest(t, path, []adminRestartNotification{
		{
			Platform:  platform.PlatformFeishu,
			AccountID: "cli_a",
			ChatID:    "oc_chat",
			UserID:    "ou_admin",
			CreatedAt: time.Now(),
		},
	})
	reply := newAdminCommandTestReplier()
	fakePlatform := &adminRestartNotifyPlatform{
		platformName: platform.PlatformFeishu,
		accountID:    "cli_a",
		reply:        reply,
	}
	registry := platform.NewRegistry([]platform.RegistryEntry{
		{Platform: fakePlatform, Access: platform.NewAccessControl([]string{"ou_admin"})},
	})

	DeliverPendingRestartNotifications(context.Background(), registry, "v0.1.test")

	if fakePlatform.chatID != "oc_chat" {
		t.Fatalf("chatID=%q, want oc_chat", fakePlatform.chatID)
	}
	texts := reply.waitTexts(t, 1)
	if !strings.Contains(texts[0], "WeClaw 已重启完成") || !strings.Contains(texts[0], "版本：v0.1.test") {
		t.Fatalf("reply texts=%#v, want restart completion notice with version", texts)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pending notification file still exists, stat err=%v", err)
	}
}

func TestDeliverPendingRestartNotificationsKeepsRecordWithoutReplier(t *testing.T) {
	path := useAdminRestartNotificationPath(t)
	writeAdminRestartNotificationsForTest(t, path, []adminRestartNotification{
		{
			Platform:  platform.PlatformFeishu,
			AccountID: "cli_a",
			ChatID:    "oc_chat",
			UserID:    "ou_admin",
			CreatedAt: time.Now(),
		},
	})
	registry := platform.NewRegistry([]platform.RegistryEntry{
		{
			Platform: &adminRestartNotifyPlatform{
				platformName: platform.PlatformFeishu,
				accountID:    "cli_b",
				reply:        newAdminCommandTestReplier(),
			},
			Access: platform.NewAccessControl([]string{"ou_admin"}),
		},
	})

	DeliverPendingRestartNotifications(context.Background(), registry, "v0.1.test")

	notifications := readAdminRestartNotifications(t, path)
	if len(notifications) != 1 || notifications[0].AccountID != "cli_a" {
		t.Fatalf("notifications=%#v, want original notification kept", notifications)
	}
}

func useAdminRestartNotificationPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin-restart-notifications.json")
	old := adminRestartNotificationPathFunc
	adminRestartNotificationPathFunc = func() (string, error) {
		return path, nil
	}
	t.Cleanup(func() {
		adminRestartNotificationPathFunc = old
	})
	return path
}

func readAdminRestartNotifications(t *testing.T, path string) []adminRestartNotification {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pending notification file: %v", err)
	}
	var notifications []adminRestartNotification
	if err := json.Unmarshal(data, &notifications); err != nil {
		t.Fatalf("decode pending notification file: %v", err)
	}
	return notifications
}

func writeAdminRestartNotificationsForTest(t *testing.T, path string, notifications []adminRestartNotification) {
	t.Helper()
	data, err := json.Marshal(notifications)
	if err != nil {
		t.Fatalf("encode pending notification file: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create pending notification dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write pending notification file: %v", err)
	}
}

type adminRestartNotifyPlatform struct {
	platformName platform.PlatformName
	accountID    string
	reply        platform.Replier
	chatID       string
}

func (p *adminRestartNotifyPlatform) Name() platform.PlatformName {
	return p.platformName
}

func (p *adminRestartNotifyPlatform) AccountID() string {
	return p.accountID
}

func (p *adminRestartNotifyPlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}

func (p *adminRestartNotifyPlatform) Run(ctx context.Context, dispatch platform.DispatchFunc) error {
	return nil
}

func (p *adminRestartNotifyPlatform) NewReplier(chatID string) platform.Replier {
	p.chatID = chatID
	return p.reply
}
