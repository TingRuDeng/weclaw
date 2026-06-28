package feishu

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const permissionGuideCooldown = 60 * time.Second

var permissionErrorCodes = map[int]struct{}{
	99991400: {},
}

// IsPermissionErrorCode 判断飞书错误码是否属于权限不足。
func IsPermissionErrorCode(code int) bool {
	_, ok := permissionErrorCodes[code]
	return ok
}

// PermissionGuideMessage 返回飞书权限开通引导文案。
func PermissionGuideMessage(appID string) string {
	return fmt.Sprintf("飞书应用权限不足，请在开发者后台开通权限并发布版本：https://open.feishu.cn/app/%s/permission", appID)
}

type permissionGuideLimiter struct {
	appID    string
	cooldown time.Duration
	now      func() time.Time
	mu       sync.Mutex
	last     time.Time
}

// newPermissionGuideLimiter 创建权限引导冷却器，避免同类错误刷屏。
func newPermissionGuideLimiter(appID string) *permissionGuideLimiter {
	return &permissionGuideLimiter{appID: appID, cooldown: permissionGuideCooldown, now: time.Now}
}

// MessageForCode 在权限错误且冷却已过时返回引导文案。
func (l *permissionGuideLimiter) MessageForCode(code int) (string, bool) {
	if !IsPermissionErrorCode(code) {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.last.IsZero() && now.Sub(l.last) < l.cooldown {
		return "", false
	}
	l.last = now
	return PermissionGuideMessage(l.appID), true
}

type feishuAPIError struct {
	appID string
	code  int
	msg   string
}

func (e *feishuAPIError) Error() string {
	if IsPermissionErrorCode(e.code) {
		return fmt.Sprintf("%s；code=%d msg=%s", PermissionGuideMessage(e.appID), e.code, e.msg)
	}
	return fmt.Sprintf("feishu api error: code=%d msg=%s", e.code, e.msg)
}

// feishuErrorCode 提取飞书 API 错误码。
func feishuErrorCode(err error) (int, bool) {
	var apiErr *feishuAPIError
	if errors.As(err, &apiErr) {
		return apiErr.code, true
	}
	return 0, false
}

// formatFeishuAPIError 生成不包含 app_secret 的飞书 API 错误。
func formatFeishuAPIError(appID string, code int, msg string) error {
	return &feishuAPIError{appID: appID, code: code, msg: msg}
}
