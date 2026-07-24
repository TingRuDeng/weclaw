package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const adminRestartNotificationFile = "admin-restart-notifications.json"

var adminRestartNotificationPathFunc = defaultAdminRestartNotificationPath

type adminRestartNotification struct {
	Platform  platform.PlatformName            `json:"platform"`
	AccountID string                           `json:"account_id,omitempty"`
	ChatID    string                           `json:"chat_id"`
	UserID    string                           `json:"user_id,omitempty"`
	Stream    *platform.DurableStreamReference `json:"stream,omitempty"`
	CreatedAt time.Time                        `json:"created_at"`
}

// recordAdminRestartNotification 保存重启前的会话路由，供新进程启动后回写完成通知。
func recordAdminRestartNotification(msg platform.IncomingMessage, stream platform.Stream) (bool, error) {
	notification, ok := newAdminRestartNotification(msg)
	if !ok {
		return false, fmt.Errorf("缺少重启完成通知的会话路由")
	}
	if exporter, ok := stream.(platform.DurableStreamReferenceExporter); ok {
		reference, err := exporter.DurableReference()
		if err != nil {
			return false, fmt.Errorf("导出重启卡片引用: %w", err)
		}
		if reference.Kind != "" {
			notification.Stream = &reference
		}
	}
	notifications, err := loadAdminRestartNotifications()
	if err != nil {
		return false, err
	}
	notifications = append(notifications, notification)
	if err := writeAdminRestartNotifications(notifications); err != nil {
		return false, err
	}
	return notification.Stream != nil, nil
}

// DeliverPendingRestartNotifications 在新进程启动后发送上一次远程重启的完成通知。
func DeliverPendingRestartNotifications(ctx context.Context, registry *platform.Registry, version string) {
	deliverPendingRestartNotifications(ctx, registry, version, nil)
}

// DeliverPendingRestartNotifications 在新进程启动后优先回写原卡；失败时交给终态 outbox。
func (h *Handler) DeliverPendingRestartNotifications(ctx context.Context, registry *platform.Registry, version string) {
	deliverPendingRestartNotifications(ctx, registry, version, h.currentTerminalOutbox())
}

func deliverPendingRestartNotifications(ctx context.Context, registry *platform.Registry, version string, outbox *terminalOutbox) {
	notifications, err := loadAdminRestartNotifications()
	if err != nil {
		log.Printf("[admin-restart] failed to load pending notifications: %v", err)
		return
	}
	if len(notifications) == 0 {
		return
	}
	remaining := make([]adminRestartNotification, 0, len(notifications))
	for _, notification := range notifications {
		if !sendAdminRestartCompletion(ctx, registry, version, notification, outbox) {
			remaining = append(remaining, notification)
		}
	}
	if err := replaceAdminRestartNotifications(remaining); err != nil {
		log.Printf("[admin-restart] failed to update pending notifications: %v", err)
	}
}

// newAdminRestartNotification 从入站消息提取跨进程可复用的最小会话路由。
func newAdminRestartNotification(msg platform.IncomingMessage) (adminRestartNotification, bool) {
	chatID := firstNonEmptyString(msg.ChatID, msg.UserID)
	if msg.Platform == "" || chatID == "" {
		return adminRestartNotification{}, false
	}
	return adminRestartNotification{
		Platform:  msg.Platform,
		AccountID: strings.TrimSpace(msg.AccountID),
		ChatID:    chatID,
		UserID:    strings.TrimSpace(msg.UserID),
		CreatedAt: time.Now(),
	}, true
}

// loadAdminRestartNotifications 读取尚未成功回写的远程重启完成通知。
func loadAdminRestartNotifications() ([]adminRestartNotification, error) {
	path, err := adminRestartNotificationPathFunc()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var notifications []adminRestartNotification
	if err := json.Unmarshal(data, &notifications); err != nil {
		return nil, err
	}
	return notifications, nil
}

// writeAdminRestartNotifications 以受限权限写入待通知记录，避免泄露本地状态。
func writeAdminRestartNotifications(notifications []adminRestartNotification) error {
	path, err := adminRestartNotificationPathFunc()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(notifications, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// replaceAdminRestartNotifications 用发送失败的记录替换原文件，成功发送后清理文件。
func replaceAdminRestartNotifications(notifications []adminRestartNotification) error {
	path, err := adminRestartNotificationPathFunc()
	if err != nil {
		return err
	}
	if len(notifications) > 0 {
		return writeAdminRestartNotifications(notifications)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// sendAdminRestartCompletion 向原平台会话发送重启完成通知，返回值用于决定是否保留记录重试。
func sendAdminRestartCompletion(ctx context.Context, registry *platform.Registry, version string, notification adminRestartNotification, outbox *terminalOutbox) bool {
	reply, ok := registry.ReplierFor(notification.Platform, notification.AccountID, notification.ChatID)
	if !ok {
		log.Printf("[admin-restart] no outbound replier for platform=%s account=%s chat=%s", notification.Platform, notification.AccountID, notification.ChatID)
		return false
	}
	content := adminRestartCompletionText(version)
	if notification.Stream != nil {
		if deliverAdminRestartCard(ctx, reply, notification, content, outbox) {
			return true
		}
		log.Printf("[admin-restart] failed to update original restart card for %s", notification.UserID)
		return false
	}
	if err := reply.SendText(ctx, content); err != nil {
		log.Printf("[admin-restart] failed to send completion notification to %s: %v", notification.UserID, err)
		return enqueueAdminRestartText(ctx, reply, notification, content, outbox)
	}
	return true
}

func deliverAdminRestartCard(ctx context.Context, reply platform.Replier, notification adminRestartNotification, content string, outbox *terminalOutbox) bool {
	preparer, ok := reply.(platform.DurableStreamTerminalPreparer)
	if !ok {
		return deliverAdminRestartTextFallback(ctx, reply, notification, content, outbox)
	}
	checkpoint, err := preparer.PrepareTerminalFromReference(*notification.Stream, content, false)
	if err != nil || checkpoint.Kind == "" {
		if err != nil {
			log.Printf("[admin-restart] failed to prepare original card terminal: %v", err)
		}
		return deliverAdminRestartTextFallback(ctx, reply, notification, content, outbox)
	}
	durable, ok := reply.(platform.DurableTerminalReplier)
	if ok {
		if err := durable.DeliverTerminal(ctx, checkpoint); err == nil {
			return true
		} else {
			log.Printf("[admin-restart] failed to deliver original card terminal: %v", err)
		}
	}
	if outbox != nil {
		route := adminRestartDeliveryRoute(notification)
		if err := outbox.enqueueAndAttempt(ctx, terminalOutboxDraft{
			Route: route, AgentName: "weclaw-restart", Checkpoint: &checkpoint,
		}, reply); err == nil {
			return true
		} else {
			log.Printf("[admin-restart] failed to enqueue original card terminal: %v", err)
		}
	}
	return sendAdminRestartText(ctx, reply, notification, content)
}

func enqueueAdminRestartText(ctx context.Context, reply platform.Replier, notification adminRestartNotification, content string, outbox *terminalOutbox) bool {
	if outbox == nil {
		return false
	}
	if err := outbox.enqueueAndAttempt(ctx, terminalOutboxDraft{
		Route: adminRestartDeliveryRoute(notification), AgentName: "weclaw-restart", Text: content,
	}, reply); err != nil {
		log.Printf("[admin-restart] failed to enqueue completion text: %v", err)
		return false
	}
	return true
}

func deliverAdminRestartTextFallback(ctx context.Context, reply platform.Replier, notification adminRestartNotification, content string, outbox *terminalOutbox) bool {
	if enqueueAdminRestartText(ctx, reply, notification, content, outbox) {
		return true
	}
	return sendAdminRestartText(ctx, reply, notification, content)
}

func sendAdminRestartText(ctx context.Context, reply platform.Replier, notification adminRestartNotification, content string) bool {
	if err := reply.SendText(ctx, content); err != nil {
		log.Printf("[admin-restart] failed to send fallback completion to %s: %v", notification.UserID, err)
		return false
	}
	return true
}

func adminRestartDeliveryRoute(notification adminRestartNotification) platform.DeliveryRoute {
	return platform.DeliveryRoute{
		Platform: notification.Platform, AccountID: notification.AccountID, ChatID: notification.ChatID,
	}
}

// adminRestartCompletionText 生成用户可直接理解的重启完成消息。
func adminRestartCompletionText(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "WeClaw 已重启完成，可以继续使用。"
	}
	return "WeClaw 已重启完成，可以继续使用。\n版本：" + version
}

// defaultAdminRestartNotificationPath 返回跨进程共享的重启通知状态文件路径。
func defaultAdminRestartNotificationPath() (string, error) {
	home := strings.TrimSpace(defaultDataDir())
	if home == "" {
		return "", fmt.Errorf("用户主目录为空")
	}
	return filepath.Join(home, "state", adminRestartNotificationFile), nil
}

// firstNonEmptyString 返回第一个去空白后非空的字符串，用于统一路由兜底顺序。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
