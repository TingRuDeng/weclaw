package messaging

import "github.com/fastclaw-ai/weclaw/platform"

// ObserveFeishuIdentity 接收 Registry 的访问控制前身份观察回调。
func (h *Handler) ObserveFeishuIdentity(msg platform.IncomingMessage) {
	h.ensureFeishuIdentities().Remember(msg)
}
