package feishu

// permanentResourceDownloadError 标记重投同一事件也无法恢复的飞书资源错误。
type permanentResourceDownloadError struct{ message string }

func (e permanentResourceDownloadError) Error() string   { return e.message }
func (e permanentResourceDownloadError) Permanent() bool { return true }

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
