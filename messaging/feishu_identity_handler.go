package messaging

import (
	"fmt"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

// ObserveFeishuIdentity 接收 Registry 的访问控制前身份观察回调。
func (h *Handler) ObserveFeishuIdentity(msg platform.IncomingMessage) {
	h.ensureFeishuIdentities().Remember(msg)
}

// ObserveDeniedFeishuIdentity 记录未授权飞书身份并返回可交给管理员的授权码提示。
func (h *Handler) ObserveDeniedFeishuIdentity(msg platform.IncomingMessage) string {
	store := h.ensureFeishuIdentities()
	store.Remember(msg)
	identity, ok := extractFeishuIdentity(msg)
	if !ok {
		return ""
	}
	record, ok := store.IssueAuthCode(identity.Key, time.Now().UTC())
	if !ok || record.AuthCode == "" {
		return ""
	}
	return fmt.Sprintf("当前账号无权限，请联系管理员授权。\n授权码: %s", record.AuthCode)
}
