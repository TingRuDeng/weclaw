package messaging

import (
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

func (h *Handler) refreshFeishuAccountAccess(accountID string, allowedUsers []string) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	h.mu.RLock()
	update := h.platformAccessUpdater
	registry := h.platformRegistry
	h.mu.RUnlock()
	if update == nil {
		return
	}
	h.codexFollowerDeliveryMu.Lock()
	update(platform.PlatformFeishu, accountID, append([]string(nil), allowedUsers...))
	h.detachUnauthorizedCodexFollowersLocked(registry, platform.PlatformFeishu, accountID)
	h.codexFollowerDeliveryMu.Unlock()
	h.wakeCodexFollowerReconciler()
}

func (h *Handler) detachUnauthorizedCodexFollowers(registry *platform.Registry, platformName platform.PlatformName, accountID string) {
	h.codexFollowerDeliveryMu.Lock()
	defer h.codexFollowerDeliveryMu.Unlock()
	h.detachUnauthorizedCodexFollowersLocked(registry, platformName, accountID)
}

func (h *Handler) detachUnauthorizedCodexFollowersLocked(registry *platform.Registry, platformName platform.PlatformName, accountID string) {
	if registry == nil {
		return
	}
	for _, snapshot := range h.ensureCodexSessions().followerSnapshots() {
		if snapshot.Target.DeliveryRoute.Platform != platformName ||
			strings.TrimSpace(snapshot.Target.DeliveryRoute.AccountID) != strings.TrimSpace(accountID) ||
			h.codexFollowerAuthorized(registry, snapshot) {
			continue
		}
		cleared, err := h.ensureCodexSessions().clearFollowerIfMatches(snapshot)
		if err != nil {
			log.Printf("[codex-follower] 持久化撤权端点失败 route=%q thread=%q: %v",
				snapshot.BindingKey, snapshot.Target.ThreadID, err)
		}
		detached := h.detachCodexFrontendTaskForAuthorizationRevocation(
			snapshot.ConversationID, snapshot.RouteUserID, snapshot.Target.ThreadID,
		)
		if detached.progress != nil {
			detached.progress.discardDetachedWithoutTerminal()
		}
		if cleared || detached.detached {
			log.Printf("[codex-follower] 已停止未授权端点同步 route=%q thread=%q",
				snapshot.BindingKey, snapshot.Target.ThreadID)
		}
	}
}

func (h *Handler) codexFollowerIdentityAuthorized(platformName platform.PlatformName, accountID string, identity string) bool {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return false
	}
	h.mu.RLock()
	registry := h.platformRegistry
	h.mu.RUnlock()
	// 未注入 Registry 的 Handler 只能由同包内已经携带不可伪造 access grant 的
	// 入站消息抵达这里；正式服务始终注入 Registry，并在提交 follower 前复核最新权限。
	if registry == nil {
		return true
	}
	return registry.AllowsStoredIdentity(platformName, strings.TrimSpace(accountID), []string{identity})
}

// ReconcilePlatformAccess 在配置热重载完成 Registry 更新后同步撤销失效的主动回推端点。
func (h *Handler) ReconcilePlatformAccess(platformName platform.PlatformName, accountID string) {
	h.mu.RLock()
	registry := h.platformRegistry
	h.mu.RUnlock()
	h.detachUnauthorizedCodexFollowers(registry, platformName, accountID)
	h.wakeCodexFollowerReconciler()
}
