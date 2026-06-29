package feishu

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const permissionGuideCooldown = 60 * time.Second

var permissionErrorCodes = map[int]struct{}{
	99991400: {},
	99991401: {},
	99991663: {},
	99991672: {},
	99991670: {},
	99991668: {},
}

var permissionErrorTextMarkers = []string{
	"permission",
	"权限",
	"scope",
	"forbidden",
	"not authorized",
	"no access",
}

type permissionScope struct {
	Name  string
	Label string
}

var requiredPermissionScopes = []permissionScope{
	{Name: "im:message", Label: "获取与发送单聊、群组消息"},
	{Name: "im:message:send_as_bot", Label: "以应用身份发消息"},
	{Name: "im:resource", Label: "获取与上传图片或文件资源"},
	{Name: "im:chat", Label: "获取会话信息"},
}

var optionalPermissionScopes = []permissionScope{
	{Name: "cardkit:card", Label: "CardKit 卡片管理"},
}

// IsPermissionErrorCode 判断飞书错误码是否属于权限不足。
func IsPermissionErrorCode(code int) bool {
	_, ok := permissionErrorCodes[code]
	return ok
}

// IsPermissionError 统一判断飞书错误是否为权限不足，供发送、CardKit 和校验路径复用。
func IsPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if code, ok := feishuErrorCode(err); ok {
		return IsPermissionErrorCode(code)
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range permissionErrorTextMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

// PermissionGuideMessage 返回飞书权限开通引导文案。
func PermissionGuideMessage(appID string) string {
	return buildPermissionGuideText(appID, true)
}

func buildPermissionURL(appID string) string {
	return fmt.Sprintf("https://open.feishu.cn/app/%s/permission", strings.TrimSpace(appID))
}

func buildPermissionGuideText(appID string, includeCardKit bool) string {
	var lines []string
	lines = append(lines,
		"飞书应用权限不足，无法完成当前操作。",
		"请在飞书开放平台开通权限并发布版本："+buildPermissionURL(appID),
		"",
		"必需权限：",
	)
	lines = appendScopeLines(lines, requiredPermissionScopes)
	if includeCardKit {
		lines = append(lines, "", "可选权限：")
		lines = appendScopeLines(lines, optionalPermissionScopes)
	}
	lines = append(lines, "", "权限修改后需创建版本并发布，管理员审批后生效。")
	return strings.Join(lines, "\n")
}

func appendScopeLines(lines []string, scopes []permissionScope) []string {
	for _, scope := range scopes {
		lines = append(lines, fmt.Sprintf("- %s：%s", scope.Name, scope.Label))
	}
	return lines
}

func buildPermissionGuideCard(appID string, includeCardKit bool) (string, error) {
	content := buildPermissionGuideText(appID, includeCardKit)
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":     true,
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "飞书应用权限不足",
			},
			"template": "orange",
		},
		"body": map[string]any{
			"direction": "vertical",
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal feishu permission guide card: %w", err)
	}
	return string(data), nil
}

func logPermissionGuide(appID string) {
	log.Printf("[feishu] 权限设置页: %s", buildPermissionURL(appID))
	log.Printf("[feishu] 必需权限: %s", scopeNames(requiredPermissionScopes))
	log.Printf("[feishu] 可选权限: %s", scopeNames(optionalPermissionScopes))
	log.Printf("[feishu] 权限修改后需创建版本并发布，管理员审批后生效")
}

func scopeNames(scopes []permissionScope) string {
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, scope.Name)
	}
	return strings.Join(names, ", ")
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
	if !IsPermissionError(formatFeishuAPIError(l.appID, code, "")) {
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
	if IsPermissionError(e) {
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
