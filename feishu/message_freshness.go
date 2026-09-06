package feishu

import (
	"context"
	"log"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (a *Adapter) notifyStaleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if _, valid := feishuMessageCreateTimeValue(event); !valid {
		return nil
	}
	if ExtractFeishuSessionScope(event).ChatType != "p2p" {
		return nil
	}
	// Feedback requires explicit account authorization, even before Registry dispatch.
	a.accessMu.RLock()
	configured := a.accessSet
	a.accessMu.RUnlock()
	if !configured {
		return nil
	}
	msg, _, reservation, ok := a.toIncomingEnvelopeFromMessage(event)
	if !ok {
		return nil
	}
	if !a.allowIncomingMessage(msg) {
		reservation.release()
		return nil
	}
	if err := completeFeishuDedupReservation(reservation); err != nil {
		reservation.release()
		return err
	}
	now := a.now()
	key := strings.Join([]string{msg.AccountID, msg.ChatID, msg.UserID}, "\x00")
	a.staleNoticeMu.Lock()
	if a.staleNotices == nil {
		a.staleNotices = make(map[string]time.Time)
	}
	for k, last := range a.staleNotices {
		if now.Sub(last) >= time.Minute {
			delete(a.staleNotices, k)
		}
	}
	_, limited := a.staleNotices[key]
	if !limited {
		a.staleNotices[key] = now
	}
	a.staleNoticeMu.Unlock()
	if limited {
		return nil
	}
	noticeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), feishuMessageNoticeTimeout)
	defer cancel()
	if err := a.newScopedReplier(msg).SendText(noticeCtx, "本次收到的较早消息已跳过自动执行；如请求尚未完成，请确认后重新发送。"); err != nil {
		log.Printf("[feishu] failed to send stale message notice: %v", err)
		return err
	}
	return nil
}

// DefaultMessageMaxAge 是飞书普通消息默认允许的最大投递延迟。
const DefaultMessageMaxAge = 2 * time.Minute

const (
	staleReasonBeforeRun = "created_before_run"
	staleReasonMaxAge    = "max_age_exceeded"
)

// SetMaxMessageAge 设置普通消息最大年龄，0 表示显式关闭时效保护。
func (a *Adapter) SetMaxMessageAge(maxAge time.Duration) {
	a.maxMessageAge = maxAge
}

// beginMessageAcceptance 建立本次长连接运行周期的消息接收水位。
func (a *Adapter) beginMessageAcceptance() {
	if a.maxMessageAge <= 0 {
		a.messageAcceptAfter = time.Time{}
		return
	}
	a.messageAcceptAfter = a.now()
}

// shouldIgnoreStaleMessage 在消息进入解析和命令路由前执行时效校验。
func (a *Adapter) shouldIgnoreStaleMessage(event *larkim.P2MessageReceiveV1) bool {
	if a.maxMessageAge <= 0 || a.messageAcceptAfter.IsZero() {
		return false
	}
	receivedAt := a.now()
	createdAt, ok := feishuMessageCreateTimeValue(event)
	if !ok {
		a.logInvalidMessageTime(event, receivedAt)
		return true
	}
	return a.rejectStaleMessage(event, createdAt, receivedAt)
}

// staleMessageReason 返回消息过期原因，空字符串表示消息仍可处理。
func (a *Adapter) staleMessageReason(createdAt time.Time, receivedAt time.Time) string {
	if createdAt.Before(a.messageAcceptAfter) {
		return staleReasonBeforeRun
	}
	if receivedAt.Sub(createdAt) > a.maxMessageAge {
		return staleReasonMaxAge
	}
	return ""
}

// logInvalidMessageTime 记录非法时间戳，但不记录消息正文。
func (a *Adapter) logInvalidMessageTime(event *larkim.P2MessageReceiveV1, receivedAt time.Time) {
	scope := ExtractFeishuSessionScope(event)
	log.Printf("[feishu] ignored message with invalid create time: account=%s chat=%s message=%s create_time=%q received_at=%s",
		a.creds.AppID, scope.ChatID, scope.MessageID, feishuMessageCreateTime(event), receivedAt.Format(time.RFC3339Nano))
}

// rejectStaleMessage 记录历史消息的时间边界，并返回拦截结果。
func (a *Adapter) rejectStaleMessage(event *larkim.P2MessageReceiveV1, createdAt time.Time, receivedAt time.Time) bool {
	reason := a.staleMessageReason(createdAt, receivedAt)
	if reason == "" {
		return false
	}
	scope := ExtractFeishuSessionScope(event)
	log.Printf("[feishu] ignored stale message event: account=%s chat=%s message=%s created_at=%s received_at=%s age=%s accept_after=%s max_age=%s reason=%s",
		a.creds.AppID, scope.ChatID, scope.MessageID, createdAt.Format(time.RFC3339Nano), receivedAt.Format(time.RFC3339Nano),
		receivedAt.Sub(createdAt), a.messageAcceptAfter.Format(time.RFC3339Nano), a.maxMessageAge, reason)
	return true
}
