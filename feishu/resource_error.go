package feishu

import "fmt"

// permanentResourceDownloadError 标记重投同一事件也无法恢复的飞书资源错误。
type permanentResourceDownloadError struct {
	message string
	cause   error
}

func (e permanentResourceDownloadError) Error() string   { return e.message }
func (e permanentResourceDownloadError) Permanent() bool { return true }
func (e permanentResourceDownloadError) Unwrap() error   { return e.cause }

// newFeishuResourceAPIError 保留飞书结构化错误码，供权限和重试策略统一判断。
func newFeishuResourceAPIError(appID string, fileKey string, code int, message string) error {
	apiErr := formatFeishuAPIError(appID, code, message)
	err := fmt.Errorf("download feishu resource %s failed: %w", fileKey, apiErr)
	if isPermanentFeishuResourceCode(code) {
		return permanentResourceDownloadError{message: err.Error(), cause: err}
	}
	return err
}

// isPermanentFeishuResourceCode 只收录飞书官方明确要求修正输入或配置的资源错误码。
func isPermanentFeishuResourceCode(code int) bool {
	switch code {
	case 230110, 234001, 234002, 234003, 234004, 234009,
		234037, 234038, 234040, 234041, 234043:
		return true
	default:
		return false
	}
}
